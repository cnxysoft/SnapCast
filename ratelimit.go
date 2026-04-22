package main

import (
	"net/netip"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type RateLimiter struct {
	mu       sync.RWMutex
	window   time.Duration
	maxReqs  int
	maskBits int
	requests map[uint64][]int64 // keyed by masked IP, value is slice of request timestamps
	enabled  bool
}

var globalRateLimiter = &RateLimiter{
	requests: make(map[uint64][]int64),
}

func ConfigureRateLimiter(enabled bool, window time.Duration, maxReqs int, maskBits int) {
	globalRateLimiter.mu.Lock()
	defer globalRateLimiter.mu.Unlock()

	globalRateLimiter.enabled = enabled
	globalRateLimiter.window = window
	globalRateLimiter.maxReqs = maxReqs
	globalRateLimiter.maskBits = maskBits
	globalRateLimiter.requests = make(map[uint64][]int64)
}

// cleanup 定期清理过期的请求记录
func (r *RateLimiter) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UnixMilli()
	cutoff := now - r.window.Milliseconds()

	for key, times := range r.requests {
		var valid []int64
		for _, t := range times {
			if t > cutoff {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(r.requests, key)
		} else {
			r.requests[key] = valid
		}
	}
}

// Allow 检查是否允许请求
func (r *RateLimiter) Allow(ip string) bool {
	if !r.enabled {
		return true
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		// 无效 IP 允许通过，由后续中间件处理
		logger.Debug("⚠️ 无法解析 IP，跳过限流检查", zap.String("ip", ip), zap.Error(err))
		return true
	}

	// 应用掩码获取 key
	var key uint64
	if addr.Is4() {
		a := addr.As4()
		bytesToUse := (r.maskBits + 7) / 8
		if bytesToUse > 4 {
			bytesToUse = 4
		}
		for i := 0; i < bytesToUse; i++ {
			bitsToKeep := r.maskBits - i*8
			if bitsToKeep >= 8 {
				key = (key << 8) | uint64(a[i])
			} else if bitsToKeep > 0 {
				key = (key << 8) | (uint64(a[i]) & (0xFF << (8 - bitsToKeep)))
			}
		}
	} else {
		// IPv6 完整掩码支持
		a := addr.As16()
		bytesToUse := (r.maskBits + 7) / 8
		if bytesToUse > 16 {
			bytesToUse = 16
		}
		for i := 0; i < bytesToUse; i++ {
			bitsToKeep := r.maskBits - i*8
			if bitsToKeep >= 8 {
				key = (key << 8) | uint64(a[i])
			} else if bitsToKeep > 0 {
				key = (key << 8) | (uint64(a[i]) & (0xFF << (8 - bitsToKeep)))
			}
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UnixMilli()
	cutoff := now - r.window.Milliseconds()

	// 清理过期记录
	var valid []int64
	for _, t := range r.requests[key] {
		if t > cutoff {
			valid = append(valid, t)
		}
	}

	if len(valid) >= r.maxReqs {
		r.requests[key] = valid
		return false
	}

	// 记录新请求
	valid = append(valid, now)
	r.requests[key] = valid
	return true
}

// StartCleanup 启动定期清理 goroutine
func StartRateLimiterCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			globalRateLimiter.cleanup()
		}
	}()
}

// RateLimitMiddleware IP 限流中间件
func RateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := GetClientIP(c)
		if !globalRateLimiter.Allow(clientIP) {
			logger.Warn("⚠️ IP 限流触发", zap.String("client_ip", clientIP))
			c.AbortWithStatusJSON(429, errResp("rate limit exceeded, try again later"))
			return
		}
		c.Next()
	}
}
