package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ====== 数据结构 ======

type CapturePayload struct {
	URL     string          `json:"url" binding:"required"`
	Options *CaptureOptions `json:"options,omitempty"`
}

type CaptureOptions struct {
	Timeout   any              `json:"timeout,omitempty"` // 自定义超时(ms)，支持数字或字符串如 "60s", "3000ms"
	UserAgent string           `json:"user_agent,omitempty"`
	Viewport  *ViewportOptions `json:"viewport,omitempty"`
	FullPage  *bool            `json:"full_page,omitempty"` // nil 表示默认 true
}

type ViewportOptions struct {
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Scale  float64 `json:"scale,omitempty"` // 设备像素比，默认 1.0
}

// ====== SSRF 防护 ======

var ssrfBlockedPrefixes = []string{
	"file://",
	"ftp://",
	"gopher://",
	"dict://",
	"javascript:",
	"data:",
}

// isPrivateIP 检查 IP 是否为内网/保留地址
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	// 10.0.0.0/8
	if ip.IsPrivate() {
		return true
	}
	// 169.254.0.0/16 (link-local)
	if bytes.HasPrefix(ip, []byte{169, 254}) {
		return true
	}
	// 127.0.0.0/8 (loopback)
	if ip.IsLoopback() {
		return true
	}
	// 0.0.0.0/8
	if ip.Equal(net.IPv4zero) {
		return true
	}
	return false
}

// validateURL 校验 URL，阻止 SSRF 攻击
func validateURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("url 不能为空")
	}

	// 检查危险协议
	lowerURL := strings.ToLower(rawURL)
	for _, prefix := range ssrfBlockedPrefixes {
		if strings.HasPrefix(lowerURL, prefix) {
			return fmt.Errorf("不支持的协议: %s", prefix)
		}
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("无效的 URL 格式: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("仅支持 http/https 协议")
	}

	if parsed.Host == "" {
		return fmt.Errorf("URL 缺少 host")
	}

	// 检查是否为内网 IP 或保留 IP
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		host = parsed.Host
	}
	if host == "" {
		return fmt.Errorf("URL 缺少 host")
	}

	// 如果是 IP 地址，检查是否为内网
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(host) {
			return fmt.Errorf("禁止访问内网 IP: %s", host)
		}
	} else {
		// 域名需要 DNS 解析，检查解析后的 IP
		ips, err := net.LookupIP(host)
		if err != nil {
			// DNS 解析失败，可能是内网域名或临时故障，放行让浏览器处理
			logger.Debug("⚠️ DNS 解析失败", zap.String("host", host), zap.Error(err))
		} else {
			for _, ip := range ips {
				if isPrivateIP(ip.String()) {
					return fmt.Errorf("域名 %s 解析为内网 IP: %s", host, ip.String())
				}
			}
		}
	}

	return nil
}

// ====== 处理器 ======

func CaptureHandler(c *gin.Context) {
	// 获取信号量，超时 5 秒
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_ = ctx // ctx 用于超时控制，实际渲染使用新 tab

	// 尝试获取并发许可
	concurrentMutex.Lock()
	if currentConcurrent >= maxConcurrent {
		concurrentMutex.Unlock()
		c.JSON(http.StatusServiceUnavailable, errResp("server busy, try again later"))
		return
	}
	currentConcurrent++
	concurrentMutex.Unlock()
	defer func() {
		concurrentMutex.Lock()
		currentConcurrent--
		concurrentMutex.Unlock()
	}()

	var payload CapturePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Error("❕ 传递参数有误", zap.Error(err))
		c.JSON(http.StatusBadRequest, errResp(err.Error()))
		return
	}

	// 校验 URL
	if err := validateURL(payload.URL); err != nil {
		logger.Warn("⛔ URL 校验失败", zap.String("url", payload.URL), zap.Error(err))
		c.JSON(http.StatusBadRequest, errResp(err.Error()))
		return
	}

	// 解析选项
	opts := payload.Options
	if opts == nil {
		opts = &CaptureOptions{}
	}

	// 默认 fullPage 为 true
	fullPage := true
	if opts.FullPage != nil {
		fullPage = *opts.FullPage
	}

	// 解析 timeout
	timeout, err := ParseDuration(opts.Timeout)
	if err != nil {
		logger.Warn("❕ 无效的 timeout 参数", zap.Any("timeout", opts.Timeout))
		c.JSON(http.StatusBadRequest, errResp(err.Error()))
		return
	}
	timeoutMs := timeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = renderTimeout.Load()
	}

	// 设置 UserAgent
	userAgent := opts.UserAgent

	logger.Debug("🔍 开始捕获", zap.String("url", payload.URL), zap.Int64("timeout", timeoutMs), zap.String("ua", userAgent), zap.Bool("full_page", fullPage))

	// 执行截图
	imgBytes, err := CaptureScreenshot(payload.URL, timeoutMs, userAgent, opts.Viewport, fullPage)
	if err != nil {
		logger.Error("❌ 捕获失败", zap.Error(err), zap.String("url", payload.URL))
		c.JSON(http.StatusInternalServerError, errResp(err.Error()))
		return
	}

	c.Header("Content-Type", "image/png")
	c.Writer.Write(imgBytes)
	c.Set("capture_url", payload.URL)
	c.Set("capture_img_size", len(imgBytes))
}

func CaptureScreenshot(rawURL string, timeoutMs int64, userAgent string, viewport *ViewportOptions, fullPage bool) ([]byte, error) {
	ctx, cancel := NewTabContext(timeoutMs)
	defer cancel()

	// 构建 chromedp 选项
	var runOpts []chromedp.Action

	// 设置 UserAgent 和 Viewport（始终设置默认 viewport 保证页面布局一致）
	width := captureViewportWidth.Load()
	height := captureViewportHeight.Load()
	scale := captureViewportScale.Load()
	if viewport != nil {
		if viewport.Width > 0 {
			width = int64(viewport.Width)
		}
		if viewport.Height > 0 {
			height = int64(viewport.Height)
		}
		if viewport.Scale > 0 {
			scale = viewport.Scale
		}
	}
	if userAgent != "" {
		runOpts = append(runOpts, emulation.SetUserAgentOverride(userAgent))
	}
	runOpts = append(runOpts, emulation.SetDeviceMetricsOverride(width, height, scale, false))

	// 导航到目标 URL
	runOpts = append(runOpts, chromedp.Navigate(rawURL))

	// 等待 body 可见
	runOpts = append(runOpts, chromedp.WaitVisible("body", chromedp.ByQuery))

	// 执行
	err := chromedp.Run(ctx, runOpts...)
	if err != nil {
		return nil, fmt.Errorf("navigate failed: %w", err)
	}

	// 根据 fullPage 决定截图方式
	var full []byte
	if fullPage {
		// 全页截图
		err = chromedp.Run(ctx, chromedp.FullScreenshot(&full, int(renderQuality.Load())))
		if err != nil {
			return nil, fmt.Errorf("full screenshot failed: %w", err)
		}
	} else {
		// 视口截图
		err = chromedp.Run(ctx, chromedp.CaptureScreenshot(&full))
		if err != nil {
			return nil, fmt.Errorf("capture screenshot failed: %w", err)
		}
	}

	if len(full) == 0 {
		return nil, fmt.Errorf("screenshot data is empty")
	}

	// 如果是全页截图，需要裁剪到 body 范围
	if fullPage {
		img, err := png.Decode(bytes.NewReader(full))
		if err != nil {
			return nil, fmt.Errorf("failed to decode screenshot: %w", err)
		}

		if img == nil {
			return nil, fmt.Errorf("decoded image is nil")
		}

		// 获取 body 范围
		var js string
		err = chromedp.Run(ctx,
			chromedp.EvaluateAsDevTools(`(function() {
				const el = document.querySelector('body');
				const r = el.getBoundingClientRect();
				const x = Math.max(0, Math.floor(r.left));
				const y = Math.max(0, Math.floor(r.top + (window.scrollY || document.documentElement.scrollTop)));
				const w = Math.ceil(r.width);
				const h = Math.ceil(r.height);
				const dpr = window.devicePixelRatio || 1;
				return JSON.stringify({ x, y, w, h, dpr });
			})()`, &js),
		)
		if err != nil {
			// 无法获取 body 范围，直接返回原始截图
			logger.Debug("⚠️ 无法获取 body 范围", zap.Error(err))
			return full, nil
		}

		type Rect struct {
			X, Y, W, H, DPR float64
		}
		var r Rect
		err = json.Unmarshal([]byte(js), &r)
		if err != nil {
			return full, nil
		}

		x := int(r.X * r.DPR)
		y := int(r.Y * r.DPR)
		w := int(r.W * r.DPR)
		h := int(r.H * r.DPR)

		bounds := img.Bounds()
		x = max(0, x)
		y = max(0, y)
		w = min(w, bounds.Dx()-x)
		h = min(h, bounds.Dy()-y)

		crop := image.Rect(x, y, x+w, y+h)
		sub := image.NewRGBA(crop)
		draw.Draw(sub, crop, img, crop.Min, draw.Src)

		var out bytes.Buffer
		err = png.Encode(&out, sub)
		if err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}

	return full, nil
}
