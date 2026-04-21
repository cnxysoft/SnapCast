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
)

func debugFields(data any) {
	b, _ := json.Marshal(data)
	var m map[string]any
	json.Unmarshal(b, &m)
	keys := reflect.ValueOf(m).MapKeys()
	if len(keys) == 0 {
		logger.Debug("🧩 渲染字段: (无)")
		return
	}
	logger.Debug(fmt.Sprintf("🧩 渲染字段: %v", keys))
}

func debugPayload(p PushPayload) {
	msg := fmt.Sprintf("📦 请求参数: site=%s type=%s output=%s", p.Site, p.Type, p.Output)
	if p.Timeout > 0 {
		msg += fmt.Sprintf(" timeout=%dms", p.Timeout)
	}
	if p.UserAgent != "" {
		msg += fmt.Sprintf(" user_agent=%s", p.UserAgent)
	}
	if p.Data != nil {
		dataJson, _ := json.Marshal(p.Data)
		msg += fmt.Sprintf(" data=%s", dataJson)
	}
	logger.Debug(msg)
}

var templateKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func selectTemplate(p PushPayload) string {
	if !templateKeyRegex.MatchString(p.Site) || !templateKeyRegex.MatchString(p.Type) {
		logger.Error(fmt.Sprintf("❌ 无效的站点或类型: site=%s, type=%s", p.Site, p.Type))
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
			err = fmt.Errorf("模板渲染 panic: %v", r)
		}
	}()
	err = tmpl.Execute(buf, data)
	return
}

func watchTemplateDir(dir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Fatal(fmt.Sprintf("❌ 监听器启动失败: %v", err))
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
							logger.Info(fmt.Sprintf("🆕 模板更新: %s → %s", key, event.Name))
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
							logger.Info(fmt.Sprintf("🗑️ 模板移除: %s → %s", key, event.Name))
						}
					}
				}
			case err = <-watcher.Errors:
				logger.Error(fmt.Sprintf("❌ 监听器错误: %v", err))
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
		logger.Info(fmt.Sprintf("✅ 支持的模板: %s → %s", k, v))
	}
	return nil
}
