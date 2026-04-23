package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	uatomic "go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ====== 数据结构 ======

type PushPayload struct {
	Site      string      `json:"site"`
	Type      string      `json:"type"`
	Output    string      `json:"output"` // "image" (default), "html", or "json"
	Data      interface{} `json:"data"`
	Timeout   any         `json:"timeout"`    // 自定义超时(ms)，支持数字或字符串如 "60s", "3000ms"
	UserAgent string      `json:"user_agent"` // 自定义 UA
}

type APIResponse struct {
	Status  string      `json:"status"`            // "ok" | "error"
	Message string      `json:"message,omitempty"` // 错误信息
	Data    interface{} `json:"data,omitempty"`    // 成功时的数据
}

func ok(data interface{}) APIResponse {
	return APIResponse{Status: "ok", Data: data}
}

func errResp(message string) APIResponse {
	return APIResponse{Status: "error", Message: message}
}

// ParseDuration 解析 duration 参数
// 支持：数字(int/int64/float64 视为毫秒)、字符串如 "60s", "3000ms", "1m"
func ParseDuration(v any) (time.Duration, error) {
	if v == nil {
		return 0, nil
	}
	switch val := v.(type) {
	case int64:
		return time.Duration(val) * time.Millisecond, nil
	case int:
		return time.Duration(val) * time.Millisecond, nil
	case float64:
		return time.Duration(val) * time.Millisecond, nil
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0, nil
		}
		// 尝试直接解析
		if d, err := time.ParseDuration(s); err == nil {
			return d, nil
		}
		// 纯数字（毫秒）
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Duration(n) * time.Millisecond, nil
		}
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	return 0, fmt.Errorf("invalid duration type: %T", v)
}

var (
	templateMap        = make(map[string]string)
	templateMutex      sync.RWMutex
	logger             *zap.Logger
	logLevel           = zap.NewAtomicLevelAt(parseLogLevel(viper.GetString("logging.level")))
	globalAuthToken       uatomic.String
	globalBrowserPath     uatomic.String
	renderTimeout         uatomic.Int64
	renderQuality        uatomic.Int32
	captureViewportWidth  uatomic.Int64
	captureViewportHeight uatomic.Int64
	captureViewportScale  uatomic.Float64
	globalAllocCtx       context.Context
	globalAllocCancel    context.CancelFunc
	concurrentMutex     sync.Mutex
	currentConcurrent   int32
	maxConcurrent       int32 // 最大并发数，可动态调整
)

// ====== 主程序 ======

func main() {
	InitLogger()
	InitConfig()
	WatchConfigChanges()
	ConfigureRateLimiter(false, time.Second, 100, 24) // 默认禁用，启动后由 ApplyDynamicConfig 配置
	StartRateLimiterCleanup(time.Minute)
	browserPath := resolveBrowserPath()
	InitGlobalAllocator(browserPath)
	defer globalAllocCancel()

	templateDir := viper.GetString("template.dir")
	err := loadTemplates(templateDir)
	if err != nil {
		logger.Fatal("❌ 加载模板失败", zap.Error(err))
		return
	}
	if viper.GetBool("template.watch") {
		watchTemplateDir(templateDir)
	}

	port := viper.GetString("server.port")
	if port == "" {
		logger.Fatal("❌ server.port 不能为空")
		return
	}
	host := viper.GetString("server.host")

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(IPFilterMiddleware())
	r.Use(RateLimitMiddleware())
	r.Use(AuthMiddleware())
	r.Use(requestLoggerMiddleware())
	r.NoRoute(func(c *gin.Context) {
		logger.Warn("❕ 路由未找到", zap.String("path", c.Request.URL.Path))
		c.JSON(http.StatusNotFound, errResp("endpoint not found"))
	})
	r.NoMethod(func(c *gin.Context) {
		logger.Warn("❕ 方法不允许", zap.String("method", c.Request.Method), zap.String("path", c.Request.URL.Path))
		c.JSON(http.StatusMethodNotAllowed, errResp("method not allowed"))
	})
	r.POST(viper.GetString("server.endpoint"), RenderHandler)
	r.POST(viper.GetString("capture.endpoint"), CaptureHandler)
	err = r.Run(host + ":" + port)
	if err != nil {
		logger.Fatal("❌ 服务器启动失败", zap.Error(err))
		return
	}
}

func InitGlobalAllocator(browserPath string) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	globalAllocCtx, globalAllocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
}

func RenderHandler(c *gin.Context) {
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

	var payload PushPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Error("❕ 传递参数有误", zap.Error(err))
		c.JSON(http.StatusBadRequest, errResp(err.Error()))
		return
	}
	if payload.Output == "" {
		payload.Output = "image"
	}
	// output 字段校验
	if payload.Output != "image" && payload.Output != "html" && payload.Output != "json" {
		logger.Warn("❕ 无效的 output 参数", zap.String("output", payload.Output))
		c.JSON(http.StatusBadRequest, errResp("invalid output: must be image, html, or json"))
		return
	}
	// 解析 timeout
	timeout, err := ParseDuration(payload.Timeout)
	if err != nil {
		logger.Warn("❕ 无效的 timeout 参数", zap.Any("timeout", payload.Timeout))
		c.JSON(http.StatusBadRequest, errResp(err.Error()))
		return
	}
	timeoutMs := timeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = renderTimeout.Load()
	}
	if logLevel.Level() == zapcore.DebugLevel {
		debugPayload(payload)
	}

	tmplPath := selectTemplate(payload)
	if tmplPath == "" {
		logger.Warn("❔ 未找到模板", zap.String("site", payload.Site), zap.String("type", payload.Type))
		c.JSON(http.StatusBadRequest, errResp("no template found"))
		return
	}

	// 渲染 HTML
	var buf bytes.Buffer
	tmpl, err := template.New(filepath.Base(tmplPath)).Funcs(funcsList).ParseFiles(tmplPath)
	if err != nil {
		logger.Error("❌ 模板解析失败", zap.Error(err), zap.String("template", tmplPath))
		c.JSON(http.StatusInternalServerError, errResp(err.Error()))
		return
	}
	if payload.Data != nil {
		if logLevel.Level() == zapcore.DebugLevel {
			debugFields(payload.Data)
		}
		err = safeExecuteTemplate(tmpl, payload.Data, &buf)
		if err != nil {
			logger.Error("❌ 模板渲染失败", zap.Error(err), zap.String("template", tmplPath))
			c.JSON(http.StatusInternalServerError, errResp(fmt.Sprintf("execute template failed: %v", err)))
			return
		}
	}

	// 输出类型: html 直接返回渲染后的 HTML
	if payload.Output == "html" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Writer.Write(buf.Bytes())
		c.Set("render_site", payload.Site)
		c.Set("render_type", payload.Type)
		c.Set("render_template", tmplPath)
		c.Set("render_output", payload.Output)
		c.Set("render_html_size", buf.Len())
		return
	}

	// 输出类型: json 执行 JS 并返回序列化结果
	if payload.Output == "json" {
		c.Header("Content-Type", "application/json")
		result, err := RenderJS(buf.String(), timeoutMs, payload.UserAgent)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errResp(err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(result))
		c.Set("render_site", payload.Site)
		c.Set("render_type", payload.Type)
		c.Set("render_template", tmplPath)
		c.Set("render_output", payload.Output)
		c.Set("render_html_size", buf.Len())
		return
	}

	// 截图
	imgBytes, err := RenderScreenshot(buf.String(), timeoutMs)
	if err != nil {
		logger.Error("❌ 截图失败", zap.Error(err), zap.String("template", tmplPath))
		c.JSON(http.StatusInternalServerError, errResp(err.Error()))
		return
	}

	c.Header("Content-Type", "image/png")
	c.Writer.Write(imgBytes)
	c.Set("render_site", payload.Site)
	c.Set("render_type", payload.Type)
	c.Set("render_template", tmplPath)
	c.Set("render_output", payload.Output)
	c.Set("render_html_size", buf.Len())
	c.Set("render_img_size", len(imgBytes))
}

func requestLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method

		fields := []zap.Field{
			zap.String("method", method),
			zap.String("path", path),
			zap.Int("status", status),
			zap.String("latency", latency.String()),
			zap.String("client_ip", clientIP),
		}

		if query != "" {
			fields = append(fields, zap.String("query", query))
		}

		if site, exists := c.Get("render_site"); exists {
			fields = append(fields, zap.String("site", site.(string)))
		}
		if tp, exists := c.Get("render_type"); exists {
			fields = append(fields, zap.String("type", tp.(string)))
		}
		if tmpl, exists := c.Get("render_template"); exists {
			fields = append(fields, zap.String("template", tmpl.(string)))
		}
		if output, exists := c.Get("render_output"); exists {
			fields = append(fields, zap.String("output", output.(string)))
		}
		if imgSize, exists := c.Get("render_img_size"); exists {
			fields = append(fields, zap.String("img_size", formatBytes(imgSize.(int))))
		} else if htmlSize, exists := c.Get("render_html_size"); exists {
			fields = append(fields, zap.String("html_size", formatBytes(htmlSize.(int))))
		}

		logger.Info("❇️ 请求结果", fields...)
	}
}

func formatBytes(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
	}
	if n >= 1024 {
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%dB", n)
}

func resolveBrowserPath() string {
	if globalBrowserPath.Load() != "" {
		return globalBrowserPath.Load()
	}

	switch runtime.GOOS {
	case "windows":
		return findWindowsChromeOrEdge()
	case "linux":
		return findLinuxChromePath()
	default:
		return ""
	}
}

func findWindowsChromeOrEdge() string {
	paths := []string{
		// Chrome (64-bit)
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		// Chrome (32-bit or fallback)
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		// Chrome (user-level install)
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),

		// Edge (system-level install, usually here even on x64)
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		// Edge (64-bit fallback)
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			logger.Info("🧭 使用浏览器路径", zap.String("path", p))
			globalBrowserPath.Store(p)
			return p
		}
	}
	logger.Warn("❕️ 未找到浏览器路径，请在配置文件中指定 render.browser_path")
	return ""
}

func findLinuxChromePath() string {
	paths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
		"/usr/bin/microsoft-edge",
		"/usr/bin/edge",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			logger.Info("🧭 使用浏览器路径", zap.String("path", p))
			globalBrowserPath.Store(p)
			return p
		}
	}
	logger.Warn("❕ 未找到浏览器路径，请在配置文件中指定 render.browser_path")
	return ""
}

func RenderScreenshot(html string, timeoutMs int64) ([]byte, error) {
	ctx, cancel := NewTabContext(timeoutMs)
	defer cancel()

	tmpFile, err := os.CreateTemp(os.TempDir(), "screenshot_*.html")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		if err := os.Remove(tmpPath); err != nil {
			logger.Debug("🗑️ 临时文件删除失败", zap.String("path", tmpPath), zap.Error(err))
		} else {
			logger.Debug("🗑️ 临时文件已删除", zap.String("path", tmpPath))
		}
	}()
	_, err = tmpFile.WriteString(html)
	if err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(tmpPath)
	fileURL := "file://" + absPath
	if runtime.GOOS != "windows" {
		fileURL = "file:///" + absPath
	}

	err = chromedp.Run(ctx,
		chromedp.Navigate(fileURL),
		emulation.SetDefaultBackgroundColorOverride().WithColor(&cdp.RGBA{R: 0, G: 0, B: 0, A: 0}),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('body').scrollIntoView({block:'start', behavior:'instant'})`, nil),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to evaluate JS: %w", err)
	}

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
		return nil, err
	}

	type Rect struct {
		X, Y, W, H, DPR float64
	}
	var r Rect
	err = json.Unmarshal([]byte(js), &r)
	if err != nil {
		return nil, err
	}

	var full []byte
	err = chromedp.Run(ctx, chromedp.FullScreenshot(&full, int(renderQuality.Load())))
	if err != nil {
		return nil, fmt.Errorf("failed to take screenshot: %w", err)
	}

	if len(full) == 0 {
		return nil, fmt.Errorf("screenshot data is empty")
	}

	img, err := png.Decode(bytes.NewReader(full))
	if err != nil {
		return nil, fmt.Errorf("failed to decode screenshot: %w", err)
	}

	if img == nil {
		return nil, fmt.Errorf("decoded image is nil")
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

func RenderJS(html string, timeoutMs int64, userAgent string) (any, error) {
	ctx, cancel := NewTabContext(timeoutMs)
	defer cancel()

	tmpFile, err := os.CreateTemp(os.TempDir(), "snapcast_js_*.html")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		if err := os.Remove(tmpPath); err != nil {
			logger.Debug("🗑️ 临时文件删除失败", zap.String("path", tmpPath), zap.Error(err))
		} else {
			logger.Debug("🗑️ 临时文件已删除", zap.String("path", tmpPath))
		}
	}()
	_, err = tmpFile.WriteString(html)
	if err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(tmpPath)
	fileURL := "file://" + absPath
	if runtime.GOOS != "windows" {
		fileURL = "file:///" + absPath
	}

	runOpts := []chromedp.Action{
		chromedp.Navigate(fileURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
	}
	if userAgent != "" {
		runOpts = append([]chromedp.Action{emulation.SetUserAgentOverride(userAgent)}, runOpts...)
	}

	err = chromedp.Run(ctx, runOpts...)
	if err != nil {
		return nil, fmt.Errorf("navigate failed: %w", err)
	}

	var jsResult string
	pollTimeout := 10 * time.Second
	if timeoutMs < 10000 {
		pollTimeout = time.Duration(timeoutMs) * time.Millisecond
	}
	err = chromedp.Run(ctx, chromedp.PollFunction(`() => {
		const r = window.SnapCastResult;
		if (r !== undefined && r !== null) return JSON.stringify(r);
		return null;
	}`, &jsResult,
		chromedp.WithPollingInterval(500*time.Millisecond),
		chromedp.WithPollingTimeout(pollTimeout),
	))
	if err != nil {
		return nil, fmt.Errorf("poll result failed: %w", err)
	}

	if jsResult == "null" || jsResult == "" {
		return nil, fmt.Errorf("SnapCastResult not set within timeout")
	}

	var result any
	if err := json.Unmarshal([]byte(jsResult), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON result: %w", err)
	}

	return result, nil
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		expected := globalAuthToken.Load()

		if expected != "" {
			token := authHeader
			if len(authHeader) >= 7 && strings.ToLower(authHeader[:6]) == "bearer" {
				token = strings.TrimSpace(authHeader[6:])
			}
			if token != expected {
				logger.Warn("🔐 认证失败", zap.String("client_ip", GetClientIP(c)))
				c.AbortWithStatusJSON(http.StatusUnauthorized, errResp("unauthorized"))
				return
			}
		}
		c.Next()
	}
}

func IPFilterMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := GetClientIP(c)
		if !globalIPList.IsAllowed(clientIP) {
			logger.Warn("⛔ IP 被拒绝", zap.String("client_ip", clientIP))
			c.AbortWithStatusJSON(http.StatusForbidden, errResp("ip forbidden"))
			return
		}
		c.Next()
	}
}

func NewTabContext(timeoutMs int64) (context.Context, context.CancelFunc) {
	browserCtx, browserCancel := chromedp.NewContext(globalAllocCtx) // 新 tab
	ctx, cancel := context.WithTimeout(browserCtx, time.Duration(timeoutMs)*time.Millisecond)
	return ctx, func() {
		cancel()
		browserCancel()
	}
}
