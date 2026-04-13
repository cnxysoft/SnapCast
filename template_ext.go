package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"
)

var funcsList = template.FuncMap{
	// ========== 基础类型转换 ==========
	"formatTime":     formatTime,
	"formatDuration": formatDuration,
	"toInt":          toInt,
	"toInt64":        toInt64,
	"toFloat64":      toFloat64,
	"toString":       toString,
	"isPositive":     isPositive,
	"now":            now,

	// ========== JSON ==========
	"toJson": func(v any) template.JS {
		logger.Debug("⚠️ toJson 被调用，存在 XSS 风险")
		b, _ := json.Marshal(v)
		return template.JS(b)
	},

	// ========== 文本处理 (Sprig 风格) ==========
	// 搜索替换
	"replace": func(s, old, new string) string {
		return strings.ReplaceAll(s, old, new)
	},
	// 文本包含判断
	"contains": func(s, substr string) bool {
		return strings.Contains(s, substr)
	},
	// 前后缀判断
	"hasPrefix": func(s, prefix string) bool {
		return strings.HasPrefix(s, prefix)
	},
	"hasSuffix": func(s, suffix string) bool {
		return strings.HasSuffix(s, suffix)
	},
	// 去空白
	"trim": func(s string) string {
		return strings.TrimSpace(s)
	},
	// 前后缀裁剪
	"trimPrefix": func(s, cutset string) string {
		return strings.TrimPrefix(s, cutset)
	},
	"trimSuffix": func(s, cutset string) string {
		return strings.TrimSuffix(s, cutset)
	},
	// 大小写转换
	"lower": func(s string) string {
		return strings.ToLower(s)
	},
	"upper": func(s string) string {
		return strings.ToUpper(s)
	},
	"title": func(s string) string {
		return strings.ToTitle(s)
	},
	// 子串截取（支持负数从末尾计算）
	"substr": func(s string, start, end int) string {
		rs := []rune(s)
		l := len(rs)
		if start < 0 {
			start = l + start
		}
		if start > l {
			start = l
		}
		if end < 0 {
			end = l + end
		}
		if end > l {
			end = l
		}
		if start >= end {
			return ""
		}
		return string(rs[start:end])
	},

	// ========== 集合操作 ==========
	"len": func(v any) int {
		switch val := v.(type) {
		case string:
			return len(val)
		case []any:
			return len(val)
		case map[string]any:
			return len(val)
		default:
			return 0
		}
	},
	// 首/尾元素（内置 index 无法以负数访问，倒序可用）
	"first": func(v any) any {
		return _index(v, 0)
	},
	"last": func(v any) any {
		return _index(v, -1)
	},
	"slice": func(v any, start, end int) []any {
		switch val := v.(type) {
		case []any:
			l := len(val)
			if start < 0 {
				start = l + start
			}
			if end < 0 {
				end = l + end
			}
			if start < 0 {
				start = 0
			}
			if end > l {
				end = l
			}
			if start >= end {
				return []any{}
			}
			return val[start:end]
		default:
			return []any{}
		}
	},

	// ========== 数学运算 ==========
	"add": func(a, b float64) float64 {
		return a + b
	},
	"sub": func(a, b float64) float64 {
		return a - b
	},
	"mul": func(a, b float64) float64 {
		return a * b
	},
	"div": func(a, b float64) float64 {
		if b == 0 {
			return math.NaN()
		}
		return a / b
	},
}

func formatTime(ts float64) string {
	t := time.Unix(int64(ts), 0).In(time.FixedZone("CST", 8*3600))
	return t.Format(time.DateTime)
}

func formatDuration(startTs float64) string {
	dur := time.Now().Unix() - int64(startTs)
	h := dur / 3600
	m := (dur % 3600) / 60
	s := dur % 60
	return fmt.Sprintf("%d小时%d分%d秒", h, m, s)
}

func toInt(v any) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case string:
		i, _ := strconv.Atoi(val)
		return i
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch val := v.(type) {
	case float64:
		return int64(val)
	case int:
		return int64(val)
	case int64:
		return val
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	default:
		return 0
	}
}

func toFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	case time.Time:
		return val.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", val)
	}
}

func isPositive(v any) bool {
	return toFloat64(v) > 0
}

func now() int64 {
	return time.Now().Unix()
}

// _index 内部使用的索引访问函数，支持负数从末尾计算
func _index(v any, i int) any {
	switch val := v.(type) {
	case []any:
		l := len(val)
		if i < 0 {
			i = l + i
		}
		if i < 0 || i >= l {
			return nil
		}
		return val[i]
	case map[string]any:
		for k, vv := range val {
			if k == fmt.Sprintf("%v", i) {
				return vv
			}
		}
		return nil
	default:
		return nil
	}
}
