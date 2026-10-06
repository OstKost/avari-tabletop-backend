package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RateLimit returns middleware that allows at most `max` requests per `window`
// from the same IP address. Excess requests get 429 Too Many Requests.
// Uses a simple fixed-window counter — good enough for auth endpoints.
func RateLimit(max int, window time.Duration) func(http.Handler) http.Handler {
	type counter struct {
		count     int
		resetAt   time.Time
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]*counter)
	)

	// Periodically clean up stale buckets (every 5× window)
	go func() {
		for range time.Tick(window * 5) {
			mu.Lock()
			now := time.Now()
			for ip, c := range buckets {
				if now.After(c.resetAt) {
					delete(buckets, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)

			mu.Lock()
			c, ok := buckets[ip]
			now := time.Now()
			if !ok || now.After(c.resetAt) {
				c = &counter{resetAt: now.Add(window)}
				buckets[ip] = c
			}
			c.count++
			over := c.count > max
			mu.Unlock()

			if over {
				w.Header().Set("Retry-After", window.String())
				http.Error(w, "Too many requests, please slow down", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the real client IP, respecting X-Forwarded-For set by
// trusted proxies. Falls back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first (leftmost) IP — that's the original client
		if i := len(xff); i > 0 {
			for j := 0; j < len(xff); j++ {
				if xff[j] == ',' {
					return xff[:j]
				}
			}
			return xff
		}
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	// Strip port from RemoteAddr
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
