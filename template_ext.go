package main

import (
	"fmt"
	"html/template"
	"strconv"
	"time"
)

var funcsList = template.FuncMap{
	"formatTime":     formatTime,
	"formatDuration": formatDuration,
	"toInt":          toInt,
	"toInt64":        toInt64,
	"toFloat64":      toFloat64,
	"toString":       toString,
	"isPositive":     isPositive,
	"now":            now,
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
