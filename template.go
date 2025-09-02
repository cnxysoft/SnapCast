package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func debugFields(data any) {
	b, _ := json.Marshal(data)
	var m map[string]any
	json.Unmarshal(b, &m)
	logger.Debug(fmt.Sprintf("🧩 渲染字段: %v", reflect.ValueOf(m).MapKeys()))
}

func selectTemplate(p PushPayload) string {
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
		logger.Fatal(fmt.Sprintf("监听器启动失败: %v", err))
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
							templateMap[key] = event.Name
							logger.Info(fmt.Sprintf("🆕 模板更新: %s → %s", key, event.Name))
						}
					}
				}
			case err = <-watcher.Errors:
				logger.Error(fmt.Sprintf("监听器错误: %v", err))
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
