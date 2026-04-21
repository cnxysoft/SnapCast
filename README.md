# SnapCast

通用动态 HTML 渲染服务

将 HTML 模板与动态数据结合，通过无头浏览器渲染为 PNG 截图、HTML 页面或执行 JavaScript 获取结果。

## 特性

- **三种输出模式**：截图（PNG）、HTML、JSON
- **JavaScript 执行**：在浏览器中执行 JS，返回序列化结果
- **热更新**：模板文件修改自动重新加载（需配置 `template.watch: true`）
- **配置热重载**：修改配置文件无需重启服务
- **自定义 User-Agent**：可为 JSON 模式指定浏览器 UA
- **自定义超时**：可按请求指定超时时间

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
  "data": { ... },
  "timeout": 5000,
  "user_agent": "自定义UA"
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `site` | 是 | 站点名称，对应模板 `{site}_{type}.html` |
| `type` | 是 | 类型名称 |
| `output` | 否 | 输出模式：`image`（默认）、`html`、`json` |
| `data` | 否 | 模板渲染数据 |
| `timeout` | 否 | 超时时间（毫秒），优先于配置文件 |
| `user_agent` | 否 | 自定义 User-Agent（JSON 模式生效） |

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

auth:
  token: ""  # Authorization header token，留空则禁用

template:
  dir: "./templates"
  watch: true  # 热更新模板

render:
  browser_path: ""  # 留空则自动检测 Chrome/Edge
  timeout_ms: 10000
  quality: 100

logging:
  level: "info"  # debug, info, warn, error
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
├── logger.go         # 日志初始化
├── snapcast.yaml     # 配置文件（自动生成）
└── templates/         # HTML 模板目录
    └── {site}_{type}.html
```

## 跨平台构建

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o dist/SnapCast.exe .

# Linux
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o dist/SnapCast .
```
