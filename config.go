package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func InitConfig() {
	ensureConfigFile("snapcast.yaml")
	viper.SetConfigName("snapcast")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".") // 当前目录
	err := viper.ReadInConfig()
	if err != nil {
		logger.Fatal("❌ 配置文件加载失败", zap.Error(err))
	}
	ApplyDynamicConfig()
	logger.Info("✅ 配置文件加载成功", zap.String("file", viper.ConfigFileUsed()))
	logActiveConfig()
}

func logActiveConfig() {
	logger.Debug("📋 生效配置")
	logger.Debug("   server", zap.String("host", viper.GetString("server.host")), zap.String("port", viper.GetString("server.port")), zap.String("endpoint", viper.GetString("server.endpoint")), zap.Int("max_connections", viper.GetInt("server.max_connections")))
	logger.Debug("   auth", zap.String("token", viper.GetString("auth.token")))
	logger.Debug("   ip_filter", zap.String("whitelist", fmt.Sprintf("%v", viper.Get("ip_filter.whitelist"))), zap.String("blacklist", fmt.Sprintf("%v", viper.Get("ip_filter.blacklist"))))
	logger.Debug("   rate_limit", zap.Bool("enabled", viper.GetBool("rate_limit.enabled")), zap.String("window", viper.GetString("rate_limit.window")), zap.Int("max_requests", viper.GetInt("rate_limit.max_requests")), zap.Int("mask", viper.GetInt("rate_limit.mask")))
	logger.Debug("   template", zap.String("dir", viper.GetString("template.dir")), zap.Bool("watch", viper.GetBool("template.watch")))
	logger.Debug("   render", zap.String("browser_path", viper.GetString("render.browser_path")), zap.Any("timeout", viper.Get("render.timeout")), zap.Int("quality", viper.GetInt("render.quality")))
	logger.Debug("   capture", zap.String("endpoint", viper.GetString("capture.endpoint")), zap.Int64("viewport_width", viper.GetInt64("capture.viewport.width")), zap.Int64("viewport_height", viper.GetInt64("capture.viewport.height")), zap.Float64("viewport_scale", viper.GetFloat64("capture.viewport.scale")))
	logger.Debug("   logging", zap.String("level", viper.GetString("logging.level")))
}

func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		defaultConfig := []byte(`
# SnapCast 服务配置
# 完整配置说明: https://github.com/xxx/SnapCast#configuration

server:
  host: "0.0.0.0"       # 监听地址
  port: 8080            # 监听端口
  endpoint: "/render"   # 渲染接口路径
  max_connections: 10   # 最大并发渲染数

auth:
  token: ""             # 认证 token，为空则禁用认证

ip_filter:
  whitelist: []         # 白名单模式，为空则使用黑名单模式
  blacklist: []         # 黑名单，支持单个 IP 或 CIDR 网段，如 192.168.1.0/24

rate_limit:
  enabled: false        # 是否启用 IP 限流
  window: "1s"          # 时间窗口，支持 "1s", "1m"
  max_requests: 60      # 单个 IP/网段每窗口最大请求数
  mask: 24              # IP 掩码位数，24=/24 网段共享限额

template:
  dir: "./templates"    # 模板目录
  watch: true           # 是否监听模板文件变化热重载

render:
  browser_path: ""      # 浏览器路径，为空则自动检测
  timeout: 10000        # 渲染超时，支持数字(毫秒)、"10s"、"10000ms"
  quality: 100          # 图片质量 0-100

capture:
  endpoint: "/capture"  # 截图接口路径
  viewport:
    width: 1920         # 默认视口宽度
    height: 1080        # 默认视口高度
    scale: 1.0          # 默认设备像素比

logging:
  level: "info"         # 日志级别: debug, info, warn, error
`)
		return os.WriteFile(path, defaultConfig, 0644)
	}
	return nil
}

func WatchConfigChanges() {
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logger.Info("🔄 配置文件变更", zap.String("file", e.Name))
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

	// 最大并发数热重载
	newMaxConn := viper.GetInt("server.max_connections")
	if newMaxConn <= 0 {
		logger.Warn("❗ server.max_connections 必须大于 0", zap.Int("max_connections", newMaxConn))
		newMaxConn = 10
	}
	concurrentMutex.Lock()
	maxConcurrent = int32(newMaxConn)
	concurrentMutex.Unlock()

	// IP 黑白名单热重载
	whitelist := viper.GetStringSlice("ip_filter.whitelist")
	blacklist := viper.GetStringSlice("ip_filter.blacklist")
	if err := ReloadIPList(whitelist, blacklist); err != nil {
		logger.Warn("⚠️ IP 列表加载失败", zap.Error(err))
	}

	// Rate Limit 配置热重载
	rlEnabled := viper.GetBool("rate_limit.enabled")
	rlWindow, _ := ParseDuration(viper.Get("rate_limit.window"))
	if rlWindow == 0 {
		rlWindow = time.Second
	}
	rlMaxReqs := viper.GetInt("rate_limit.max_requests")
	rlMask := viper.GetInt("rate_limit.mask")
	if rlMaxReqs <= 0 {
		logger.Warn("❗ rate_limit.max_requests 必须大于 0", zap.Int("max_requests", rlMaxReqs))
		rlMaxReqs = 60
	}
	if rlMask < 0 || rlMask > 32 {
		logger.Warn("❗ rate_limit.mask 必须在 0-32 之间", zap.Int("mask", rlMask))
		rlMask = 24
	}
	ConfigureRateLimiter(rlEnabled, rlWindow, rlMaxReqs, rlMask)

	// quality 范围校验 (0-100)
	newQuality := viper.GetInt32("render.quality")
	if newQuality < 0 || newQuality > 100 {
		logger.Warn("❗ render.quality 值无效", zap.Int32("quality", newQuality), zap.String("default", "100"))
		newQuality = 100
	}
	renderQuality.Store(newQuality)

	// timeout 解析 (100ms - 60s)
	newTimeout, err := ParseDuration(viper.Get("render.timeout"))
	if err != nil || newTimeout < 100*time.Millisecond || newTimeout > 60000*time.Millisecond {
		if err != nil {
			logger.Warn("❗ render.timeout 值无效", zap.Any("timeout", viper.Get("render.timeout")), zap.String("reason", err.Error()), zap.String("default", "10000"))
		} else {
			logger.Warn("❗ render.timeout 值无效", zap.Duration("timeout", newTimeout), zap.String("default", "10000"))
		}
		newTimeout = 10000 * time.Millisecond
	}
	renderTimeout.Store(newTimeout.Milliseconds())

	// capture viewport 配置（带兜底）
	width := int64(viper.GetInt("capture.viewport.width"))
	if width <= 0 {
		width = 1920
		logger.Warn("❗ capture.viewport.width 无效，使用默认值 1920", zap.Int64("value", width))
	}
	height := int64(viper.GetInt("capture.viewport.height"))
	if height <= 0 {
		height = 1080
		logger.Warn("❗ capture.viewport.height 无效，使用默认值 1080", zap.Int64("value", height))
	}
	scale := viper.GetFloat64("capture.viewport.scale")
	if scale <= 0 {
		scale = 1.0
		logger.Warn("❗ capture.viewport.scale 无效，使用默认值 1.0", zap.Float64("value", scale))
	}
	captureViewportWidth.Store(width)
	captureViewportHeight.Store(height)
	captureViewportScale.Store(scale)
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
