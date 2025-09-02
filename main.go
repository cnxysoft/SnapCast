package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ====== 数据结构 ======

type PushPayload struct {
	Site string      `json:"site"`
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

var (
	templateMap       = make(map[string]string)
	logger            *zap.Logger
	logLevel          = zap.NewAtomicLevelAt(parseLogLevel(viper.GetString("logging.level")))
	globalAuthToken   atomic.String
	globalBrowserPath atomic.String
	renderTimeout     atomic.Int64
	renderQuality     atomic.Int32
)

// ====== 主程序 ======

func main() {
	InitLogger()
	InitConfig()
	WatchConfigChanges()

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

func RenderHandler(c *gin.Context) {
	var payload PushPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tmplPath := selectTemplate(payload)
	if tmplPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no template found"})
		return
	}

	// 渲染 HTML
	var buf bytes.Buffer
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if payload.Data != nil {
		if logLevel.Level() == zapcore.DebugLevel {
			debugFields(payload.Data)
		}
		err = safeExecuteTemplate(tmpl, payload.Data, &buf)
		if err != nil {
			logger.Error(fmt.Sprintf("execute template failed: %v", err))
			return
		}
	}

	// 截图
	imgBytes, err := RenderScreenshot(buf.String())
	if err != nil {
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
			return p
		}
	}
	logger.Warn("⚠️ 未找到浏览器路径，请在配置文件中指定 render.browser_path")
	return ""
}

func RenderScreenshot(html string) ([]byte, error) {
	browserPath := resolveBrowserPath()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)

	ctx, cancel := NewRenderContext(opts, renderTimeout.Load())
	defer cancel()

	tmpFile := os.TempDir() + "/render.html"
	if err := os.WriteFile(tmpFile, []byte(html), 0644); err != nil {
		return nil, err
	}

	var buf []byte
	err := chromedp.Run(ctx,
		chromedp.Navigate("file://"+tmpFile),
		chromedp.FullScreenshot(&buf, int(renderQuality.Load())),
	)
	return buf, err
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

func NewRenderContext(opts []chromedp.ExecAllocatorOption, timeoutMs int64) (context.Context, context.CancelFunc) {
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	ctx, cancel := context.WithTimeout(browserCtx, time.Duration(timeoutMs)*time.Millisecond)

	// 返回 ctx 和一个组合 cancel（释放所有资源）
	return ctx, func() {
		cancel()
		browserCancel()
		allocCancel()
	}
}
