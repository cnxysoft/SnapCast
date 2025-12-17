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
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ====== 数据结构 ======

type PushPayload struct {
	Site string      `json:"site"`
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type ElementRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

var (
	templateMap       = make(map[string]string)
	logger            *zap.Logger
	logLevel          = zap.NewAtomicLevelAt(parseLogLevel(viper.GetString("logging.level")))
	globalAuthToken   atomic.String
	globalBrowserPath atomic.String
	renderTimeout     atomic.Int64
	renderQuality     atomic.Int32
	globalAllocCtx    context.Context
	globalAllocCancel context.CancelFunc
)

// ====== 主程序 ======

func main() {
	InitLogger()
	InitConfig()
	WatchConfigChanges()
	browserPath := resolveBrowserPath()
	InitGlobalAllocator(browserPath)
	defer globalAllocCancel()

	templateDir := viper.GetString("template.dir")
	err := loadTemplates(templateDir)
	if err != nil {
		logger.Fatal(fmt.Sprintf("❌ 加载模板失败: %v", err))
		return
	}
	if viper.GetBool("template.watch") {
		watchTemplateDir(templateDir)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Use(AuthMiddleware())
	r.POST(viper.GetString("server.endpoint"), RenderHandler)
	err = r.Run(viper.GetString("server.host") + ":" + viper.GetString("server.port"))
	if err != nil {
		logger.Fatal(fmt.Sprintf("❌ 服务器启动失败: %v", err))
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
	var payload PushPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Error(fmt.Sprintf("❌ 参数错误: %v", err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tmplPath := selectTemplate(payload)
	if tmplPath == "" {
		logger.Error(fmt.Sprintf("❌ 未找到模板: %s/%s", payload.Site, payload.Type))
		c.JSON(http.StatusBadRequest, gin.H{"error": "no template found"})
		return
	}

	// 渲染 HTML
	var buf bytes.Buffer
	tmpl, err := template.New(filepath.Base(tmplPath)).Funcs(funcsList).ParseFiles(tmplPath)
	if err != nil {
		logger.Error(fmt.Sprintf("❌ 模板解析失败: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if payload.Data != nil {
		if logLevel.Level() == zapcore.DebugLevel {
			debugFields(payload.Data)
		}
		err = safeExecuteTemplate(tmpl, payload.Data, &buf)
		if err != nil {
			logger.Error(fmt.Sprintf("❌ 模板渲染失败: %v", err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("execute template failed: %v", err)})
			return
		}
	}

	// 截图
	imgBytes, err := RenderScreenshot(buf.String())
	if err != nil {
		logger.Error(fmt.Sprintf("❌ 截图失败: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "image/png")
	c.Writer.Write(imgBytes)
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
			logger.Info(fmt.Sprintf("🧭 使用浏览器路径: %v", p))
			globalBrowserPath.Store(p)
			return p
		}
	}
	logger.Warn("⚠️ 未找到浏览器路径，请在配置文件中指定 render.browser_path")
	return ""
}

func findLinuxChromePath() string {
	paths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			logger.Info(fmt.Sprintf("🧭 使用浏览器路径: %v", p))
			globalBrowserPath.Store(p)
			return p
		}
	}
	logger.Warn("⚠️ 未找到浏览器路径，请在配置文件中指定 render.browser_path")
	return ""
}

func RenderScreenshot(html string) ([]byte, error) {
	ctx, cancel := NewTabContext(renderTimeout.Load())
	defer cancel()

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("screenshot_%d.html", time.Now().UnixNano()))
	err := os.WriteFile(tmpFile, []byte(html), 0644)
	if err != nil {
		return nil, err
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			logger.Error(fmt.Sprintf("❌ 删除临时文件失败: %v", err))
		}
	}(tmpFile)

	absPath, _ := filepath.Abs(tmpFile)
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

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		expected := globalAuthToken.Load()

		if expected != "" && authHeader != expected {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "unauthorized",
			})
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
