package main

import (
	"bytes"
	"context"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
	"html/template"
	"net/http"
	"os"
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

// ====== 主程序 ======

func main() {
	r := gin.Default()
	r.POST("/render", RenderHandler)
	r.Run(":8080")
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
