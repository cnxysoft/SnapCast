package main

import (
	"bytes"
	"context"
	"github.com/chromedp/chromedp"
	"github.com/fsnotify/fsnotify"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ====== 数据结构 ======

type PushPayload struct {
	Site    string       `json:"site"`
	Dynamic *DynamicInfo `json:"dynamic,omitempty"`
	Live    *LiveInfo    `json:"live,omitempty"`
}

type DynamicInfo struct {
	Type       int    `json:"type"`
	Id         string `json:"id"`
	Title      string `json:"title"`
	DynamicUrl string `json:"dynamic_url"`
	User       struct {
		Uid  int64  `json:"uid"`
		Name string `json:"name"`
		Face string `json:"face"`
	} `json:"user"`
}

type LiveInfo struct {
	UserInfo
	Status    int    `json:"status"`
	LiveTitle string `json:"live_title"`
	Cover     string `json:"cover"`
}

type UserInfo struct {
	Uid  int64  `json:"uid"`
	Name string `json:"name"`
	Face string `json:"face"`
}

var templateMap = make(map[string]string)

var (
	logger            *zap.Logger
	logLevel          = zap.NewAtomicLevelAt(parseLogLevel(viper.GetString("logging.level")))
	globalAuthToken   atomic.String
	globalBrowserPath atomic.String
)

// ====== 主程序 ======

func main() {
	InitLogger()
	InitConfig()
	WatchConfigChanges()

	templateDir := viper.GetString("template.dir")
	loadTemplates(templateDir)
	if viper.GetBool("template.watch") {
		watchTemplateDir(templateDir)
	}

	r := gin.Default()
	r.POST(viper.GetString("server.endpoint"), RenderHandler)
	r.Run(viper.GetString("server.host") + ":" + viper.GetString("server.port"))
}

func InitLogger() {
	cfg := zap.Config{
		Level:            logLevel,
		Development:      false,
		Encoding:         "console",
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:     "time",
			LevelKey:    "level",
			MessageKey:  "msg",
			EncodeLevel: zapcore.CapitalColorLevelEncoder,
			EncodeTime:  zapcore.ISO8601TimeEncoder,
		},
	}
	var err error
	logger, err = cfg.Build()
	if err != nil {
		panic(err)
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
	if payload.Dynamic != nil {
		tmpl.Execute(&buf, payload.Dynamic)
	} else if payload.Live != nil {
		tmpl.Execute(&buf, payload.Live)
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

func selectTemplate(p PushPayload) string {
	switch p.Site {
	case "bilibili":
		if p.Dynamic != nil {
			return "templates/bilibili_dynamic.html"
		}
		if p.Live != nil {
			return "templates/bilibili_live.html"
		}
	}
	return ""
}

func RenderScreenshot(html string) ([]byte, error) {
	// Edge 139 路径（Windows 默认安装路径）
	edgePath := `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(edgePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	tmpFile := os.TempDir() + "/render.html"
	if err := os.WriteFile(tmpFile, []byte(html), 0644); err != nil {
		return nil, err
	}

	var buf []byte
	err := chromedp.Run(ctx,
		chromedp.Navigate("file://"+tmpFile),
		chromedp.FullScreenshot(&buf, 100),
	)
	return buf, err
}

func watchTemplateDir(dir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Fatal("监听器启动失败", zap.Error(err))
	}
	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
					if strings.HasSuffix(event.Name, ".html") {
						name := filepath.Base(event.Name)
						parts := strings.Split(strings.TrimSuffix(name, ".html"), "_")
						if len(parts) == 2 {
							key := parts[0] + ":" + parts[1]
							templateMap[key] = event.Name
							logger.Info("🆕 模板更新", zap.String("Site", key), zap.String("Type", event.Name))
						}
					}
				}
			case err = <-watcher.Errors:
				logger.Error("监听器错误", zap.Error(err))
			}
		}
	}()
	watcher.Add(dir)
}

func loadTemplates(dir string) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, f := range files {
		name := f.Name()
		if strings.HasSuffix(name, ".html") {
			parts := strings.Split(strings.TrimSuffix(name, ".html"), "_")
			if len(parts) == 2 {
				key := parts[0] + ":" + parts[1] // e.g. bilibili:dynamic
				templateMap[key] = filepath.Join(dir, name)
			}
		}
	}
	for k, v := range templateMap {
		logger.Info("✅ 支持的模板", zap.String("Site:Type", k), zap.String("FileName", v))
	}
	return nil
}

func InitConfig() {
	viper.SetConfigName("snapcast")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".") // 当前目录
	err := viper.ReadInConfig()
	if err != nil {
		logger.Fatal("❌ 配置文件加载失败", zap.Error(err))
	}

	logger.Info("✅ 配置文件加载成功", zap.String("ConfigFile", viper.ConfigFileUsed()))
}

func WatchConfigChanges() {
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logger.Info("🔄 配置文件变更", zap.String("ConfigFile", e.Name))
		ApplyDynamicConfig()
	})
}

func ApplyDynamicConfig() {
	newToken := viper.GetString("auth.token")
	globalAuthToken.Store(newToken)

	newLogLevel := viper.GetString("logging.level")
	logLevel.SetLevel(parseLogLevel(newLogLevel))

	newBrowserPath := viper.GetString("render.browser_path")
	globalBrowserPath.Store(newBrowserPath)
}

func parseLogLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
