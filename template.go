package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

func debugFields(data any) {
	b, err := json.Marshal(data)
	if err != nil {
		logger.Debug("🧩 渲染字段", zap.String("status", "序列化失败"))
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		logger.Debug("🧩 渲染字段", zap.String("status", "反序列化失败"))
		return
	}
	keys := reflect.ValueOf(m).MapKeys()
	if len(keys) == 0 {
		logger.Debug("🧩 渲染字段", zap.String("status", "无"))
		return
	}
	fieldNames := make([]string, len(keys))
	for i, k := range keys {
		fieldNames[i] = k.String()
	}
	logger.Debug("🧩 渲染字段", zap.Strings("fields", fieldNames))
}

func debugPayload(p PushPayload) {
	logger.Debug("📦 请求参数",
		zap.String("site", p.Site),
		zap.String("type", p.Type),
		zap.String("output", p.Output),
	)
	if timeout, err := ParseDuration(p.Timeout); err == nil && timeout > 0 {
		logger.Debug("⏱️ 超时设置", zap.Int64("timeout_ms", timeout.Milliseconds()))
	}
	if p.UserAgent != "" {
		logger.Debug("🌐 UserAgent", zap.String("ua", p.UserAgent))
	}
	if p.Data != nil {
		logger.Debug("📊 请求数据", zap.Any("data", p.Data))
	}
}

var templateKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func selectTemplate(p PushPayload) string {
	if !templateKeyRegex.MatchString(p.Site) || !templateKeyRegex.MatchString(p.Type) {
		logger.Error("❌ 无效的站点或类型", zap.String("site", p.Site), zap.String("type", p.Type))
		return ""
	}
	templateMutex.RLock()
	defer templateMutex.RUnlock()
	key := p.Site + "/" + p.Type
	return templateMap[key]
}

func safeExecuteTemplate(tmpl *template.Template, data any, buf *bytes.Buffer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("❌ 模板渲染 panic: %v", r)
		}
	}()
	err = tmpl.Execute(buf, data)
	return
}

func watchTemplateDir(dir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Fatal("❌ 监听器启动失败", zap.Error(err))
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
							key := parts[0] + "/" + parts[1]
							templateMutex.Lock()
							templateMap[key] = event.Name
							templateMutex.Unlock()
							logger.Info("🆕 模板更新", zap.String("key", key), zap.String("path", event.Name))
						}
					}
				}
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					if strings.HasSuffix(event.Name, ".html") {
						name := filepath.Base(event.Name)
						parts := strings.Split(strings.TrimSuffix(name, ".html"), "_")
						if len(parts) == 2 {
							key := parts[0] + "/" + parts[1]
							templateMutex.Lock()
							delete(templateMap, key)
							templateMutex.Unlock()
							logger.Info("🗑️ 模板移除", zap.String("key", key), zap.String("path", event.Name))
						}
					}
				}
			case err = <-watcher.Errors:
				logger.Error("❌ 监听器错误", zap.Error(err))
			}
		}
	}()
	watcher.Add(dir)
}

func loadTemplates(dir string) error {
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		err = os.Mkdir(dir, os.ModePerm)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	templateMutex.Lock()
	defer templateMutex.Unlock()
	for _, f := range files {
		name := f.Name()
		if strings.HasSuffix(name, ".html") {
			parts := strings.Split(strings.TrimSuffix(name, ".html"), "_")
			if len(parts) == 2 {
				key := parts[0] + "/" + parts[1] // e.g. bilibili:dynamic
				templateMap[key] = filepath.Join(dir, name)
			}
		}
	}
	for k, v := range templateMap {
		logger.Info("✅ 支持的模板", zap.String("key", k), zap.String("path", v))
	}
	return nil
}
