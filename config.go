package main

import (
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"
	"os"
	"strings"
)

func InitConfig() {
	ensureConfigFile("snapcast.yaml")
	viper.SetConfigName("snapcast")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".") // 当前目录
	err := viper.ReadInConfig()
	if err != nil {
		logger.Fatal(fmt.Sprintf("❌ 配置文件加载失败: %v", err))
	}
	ApplyDynamicConfig()
	logger.Info(fmt.Sprintf("✅ 配置文件加载成功: %v", viper.ConfigFileUsed()))
}

func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		defaultConfig := []byte(`
server:
  host: "0.0.0.0"
  port: 8080
  endpoint: "/render"

auth:
  token: ""

template:
  dir: "./templates"
  watch: true

render:
  browser_path: ""
  timeout_ms: 10000
  quality: 100

logging:
  level: "info"
`)
		return os.WriteFile(path, defaultConfig, 0644)
	}
	return nil
}

func WatchConfigChanges() {
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logger.Info(fmt.Sprintf("🔄 配置文件变更: %v", e.Name))
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

	newQuality := viper.GetInt32("render.quality")
	renderQuality.Store(newQuality)

	newTimeout := viper.GetInt64("render.timeout_ms")
	renderTimeout.Store(newTimeout)
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
