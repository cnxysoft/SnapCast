package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type IPList struct {
	mu        sync.RWMutex
	whitelist []netip.Prefix
	blacklist []netip.Prefix
}

var globalIPList = &IPList{}

func ReloadIPList(whitelist, blacklist []string) error {
	globalIPList.mu.Lock()
	defer globalIPList.mu.Unlock()

	globalIPList.whitelist = nil
	globalIPList.blacklist = nil

	if err := globalIPList.parsePrefixes(whitelist, true); err != nil {
		return err
	}
	if err := globalIPList.parsePrefixes(blacklist, false); err != nil {
		return err
	}
	return nil
}

func (l *IPList) parsePrefixes(entries []string, isWhitelist bool) error {
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		var prefix netip.Prefix
		var err error

		if strings.Contains(entry, "/") {
			prefix, err = netip.ParsePrefix(entry)
		} else {
			addr, err := netip.ParseAddr(entry)
			if err != nil {
				return fmt.Errorf("invalid IP address: %s", entry)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}

		if err != nil {
			return fmt.Errorf("invalid prefix: %s", entry)
		}

		if isWhitelist {
			l.whitelist = append(l.whitelist, prefix)
		} else {
			l.blacklist = append(l.blacklist, prefix)
		}
	}
	return nil
}

func (l *IPList) IsAllowed(ipStr string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false
	}

	// 白名单启用时，只在白名单中才允许
	if len(l.whitelist) > 0 {
		for _, p := range l.whitelist {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}

	// 黑名单模式
	for _, p := range l.blacklist {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// GetClientIP 从请求中获取真实 IP
func GetClientIP(c *gin.Context) string {
	// 优先使用 X-Forwarded-For
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// X-Real-IP
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return xri
	}
	// 直接从连接获取
	ip, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return ip
}
