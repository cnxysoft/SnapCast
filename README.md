# SnapCast

通用动态 HTML 渲染服务

将 HTML 模板与动态数据结合，通过无头浏览器渲染为 PNG 截图、HTML 页面或执行 JavaScript 获取结果。

## 特性

- **三种输出模式**：截图（PNG）、HTML、JSON
- **JavaScript 执行**：在浏览器中执行 JS，返回序列化结果
- **热更新**：模板文件修改自动重新加载（需配置 `template.watch: true`）
- **配置热重载**：修改配置文件无需重启服务
- **自定义 User-Agent**：可为 JSON 模式指定浏览器 UA
- **自定义超时**：支持 `5000`、`"5s"`、`"5000ms"` 等格式
- **IP 黑白名单**：支持单个 IP 和 CIDR 网段过滤
- **统一响应格式**：`{"status": "ok/error", "data/message": ...}`
- **Bearer Token 认证**：支持 `Authorization: Bearer <token>` 格式
- **IP 限流**：滑动窗口算法，支持网段共享限额
- **并发控制**：可配置最大并发渲染数，支持热重载
- **URL 直投截图**：通过 `/capture` 端点直接访问任意 URL 截图
- **SSRF 防护**：阻止访问内网 IP、危险协议

## 快速开始

### 启动服务

```bash
# 构建
go build -ldflags="-s -w" -trimpath -o SnapCast .

# 运行
./SnapCast
```

服务默认监听 `http://0.0.0.0:8080`，渲染端点为 `/render`。

### 发送请求

```bash
curl -X POST http://127.0.0.1:8080/render \
  -H "Content-Type: application/json" \
  -d '{
    "site": "example",
    "type": "card",
    "output": "json",
    "data": {
      "name": "张三",
      "score": 9527
    }
  }'
```

## 请求格式

```json
{
  "site": "站点名",
  "type": "类型名",
  "output": "image | html | json",
  "data": { "...": "..." },
  "timeout": 5000,
  "user_agent": "自定义UA"
}
```

> `timeout` 支持：`5000`（数字，毫秒）、`"5s"`（字符串秒）、`"5000ms"`（字符串毫秒）

| 字段 | 必填 | 说明 |
|------|------|------|
| `site` | 是 | 站点名称，对应模板 `{site}_{type}.html` |
| `type` | 是 | 类型名称 |
| `output` | 否 | 输出模式：`image`（默认）、`html`、`json` |
| `data` | 否 | 模板渲染数据 |
| `timeout` | 否 | 超时时间，支持数字(毫秒)、"10s"、"5000ms" |
| `user_agent` | 否 | 自定义 User-Agent（JSON 模式生效） |

## URL 直投截图

通过 `/capture` 端点直接访问任意 URL 截图，无需准备模板：

```bash
curl -X POST http://127.0.0.1:8080/capture \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://www.baidu.com",
    "options": {
      "timeout": 10000,
      "viewport": {"width": 1920, "height": 1080, "scale": 2.0},
      "full_page": true
    }
  }'
```

返回 PNG 格式的图片。

### 请求格式

```json
{
  "url": "https://example.com",
  "options": {
    "timeout": 10000,
    "user_agent": "Mozilla/5.0 ...",
    "viewport": {
      "width": 1920,
      "height": 1080,
      "scale": 1.0
    },
    "full_page": true
  }
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `url` | 是 | 目标 URL，仅支持 http/https |
| `options.timeout` | 否 | 超时时间，默认 10000ms |
| `options.user_agent` | 否 | 自定义 User-Agent |
| `options.viewport.width` | 否 | 视口宽度，默认 1920 |
| `options.viewport.height` | 否 | 视口高度，默认 1080 |
| `options.viewport.scale` | 否 | 设备像素比，默认 1.0（2.0 为高清） |
| `options.full_page` | 否 | 全页截图，默认 true |

### SSRF 防护

自动阻止以下请求：
- 内网 IP（10.x.x.x、172.16-31.x.x、192.168.x.x、127.x.x.x）
- 保留地址（169.254.x.x、0.0.0.0）
- 危险协议（file://、ftp://、gopher:// 等）
- 解析为内网 IP 的域名

## 输出模式

### image（默认）

渲染 HTML 模板并截图返回 PNG 图片。

```bash
curl -X POST http://127.0.0.1:8080/render \
  -d '{"site":"news","type":"headline","output":"image","data":{"title":"今日头条","content":"最新新闻内容"}}'
```

### html

返回渲染后的 HTML 源代码，不执行 JS。

```bash
curl -X POST http://127.0.0.1:8080/render \
  -d '{"site":"news","type":"headline","output":"html","data":{"title":"今日头条"}}'
```

### json

在浏览器中执行 HTML/JS，捕获 `window.SnapCastResult` 返回序列化结果。

模板示例：

```html
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
</head>
<body>
<script src="https://example.com/sdk.js"></script>
<script>
window.onload = async function() {
    const result = await MySDK.getData();
    window.SnapCastResult = {
        status: result.code,
        value: result.data
    };
};
</script>
</body>
</html>
```

```bash
curl -X POST http://127.0.0.1:8080/render \
  -H "Content-Type: application/json" \
  -d '{"site":"example","type":"sdk","output":"json","user_agent":"Mozilla/5.0 (iPhone...)","data":{}}'
```

## 模板函数

模板中可使用以下函数：

### 类型转换

| 函数 | 说明 | 示例 |
|------|------|------|
| `toString` | 转换为字符串 | `{{ toString .Value }}` |
| `toInt` | 转换为整数 | `{{ toInt .Value }}` |
| `toFloat64` | 转换为浮点数 | `{{ toFloat64 .Value }}` |
| `isPositive` | 判断是否正数 | `{{ if isPositive .Count }}` |

### 时间处理

| 函数 | 说明 | 示例 |
|------|------|------|
| `formatTime` | 格式化时间戳 | `{{ formatTime .Timestamp }}` → `2024-01-01 12:00:00` |
| `formatDuration` | 格式化时长 | `{{ formatDuration .StartTs }}` → `2小时30分15秒` |
| `now` | 当前时间戳 | `{{ now }}` |

### 文本处理

| 函数 | 说明 | 示例 |
|------|------|------|
| `upper` | 转大写 | `{{ upper .Name }}` |
| `lower` | 转小写 | `{{ lower .Name }}` |
| `trim` | 去首尾空白 | `{{ trim .Text }}` |
| `replace` | 替换文本 | `{{ replace .Text "old" "new" }}` |
| `contains` | 包含判断 | `{{ contains .Text "keyword" }}` |
| `substr` | 子串截取 | `{{ substr .Text 0 10 }}` |

### 集合操作

| 函数 | 说明 | 示例 |
|------|------|------|
| `len` | 长度 | `{{ len .Items }}` |
| `first` | 首元素 | `{{ first .Items }}` |
| `last` | 尾元素 | `{{ last .Items }}` |
| `slice` | 切片 | `{{ slice .Items 0 10 }}` |

### 数学运算

| 函数 | 说明 | 示例 |
|------|------|------|
| `add` | 加法 | `{{ add .A .B }}` |
| `sub` | 减法 | `{{ sub .A .B }}` |
| `mul` | 乘法 | `{{ mul .A .B }}` |
| `div` | 除法 | `{{ div .A .B }}` |

### JSON

| 函数 | 说明 | 示例 |
|------|------|------|
| `toJson` | 序列化为 JSON | `{{ toJson .Data }}` |

## 配置文件

首次运行会自动创建 `snapcast.yaml`：

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  endpoint: "/render"
  max_connections: 10

auth:
  token: ""  # Authorization header token，留空则禁用

ip_filter:
  whitelist: []  # 白名单模式，为空则使用黑名单模式
  blacklist: []  # 黑名单，支持单个 IP 或 CIDR 网段

rate_limit:
  enabled: false  # 是否启用 IP 限流
  window: "1s"    # 时间窗口: "1s", "1m"
  max_requests: 60
  mask: 24        # IP 掩码位数，24=/24 网段共享限额

template:
  dir: "./templates"
  watch: true  # 热更新模板

render:
  browser_path: ""  # 留空则自动检测 Chrome/Edge
  timeout: 10000    # 支持数字(毫秒)、"10s"、"10000ms"
  quality: 100

capture:
  endpoint: "/capture" # 截图端点路径
  viewport:
    width: 1920        # 默认视口宽度
    height: 1080       # 默认视口高度
    scale: 1.0         # 默认设备像素比

logging:
  level: "info"  # debug, info, warn, error
```

### IP 黑白名单

支持单个 IP 和 CIDR 网段：

```yaml
ip_filter:
  whitelist: []              # 白名单模式：只有列表中的 IP 可以访问
  blacklist:                 # 黑名单模式：禁止列表中的 IP 访问
    - 192.168.1.0/24         # 网段
    - 10.0.0.1               # 单个 IP
```

### Rate Limit

IP 限流，滑动窗口算法：

```yaml
rate_limit:
  enabled: false        # 是否启用
  window: "1s"          # 时间窗口: "1s", "1m"
  max_requests: 60      # 单个 IP/网段每窗口最大请求数
  mask: 24              # IP 掩码位数，24=/24 网段共享限额
```

超限返回 429：
```json
{"status": "error", "message": "rate limit exceeded, try again later"}
```

### 调试日志

设置 `logging.level: "debug"` 开启详细日志：

```
[DEBUG] 📦 请求参数: site=example type=card output=json timeout=5000ms
[DEBUG] 🧩 渲染字段: [name score]
```

## 目录结构

```
SnapCast/
├── main.go           # 入口、HTTP 服务、渲染逻辑
├── config.go         # 配置管理
├── template.go       # 模板加载与工具函数
├── template_ext.go   # 模板函数扩展
├── ip.go             # IP 黑白名单过滤
├── ratelimit.go      # IP 限流
├── capture.go        # URL 直投截图
├── logger.go         # 日志初始化
├── snapcast.yaml     # 配置文件（自动生成）
└── templates/        # HTML 模板目录
    └── {site}_{type}.html
```

## 跨平台构建

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o dist/SnapCast.exe .

# Linux
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o dist/SnapCast .
```
