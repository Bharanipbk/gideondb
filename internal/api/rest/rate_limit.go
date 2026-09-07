package rest

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateBucket struct {
	tokens float64
	last   time.Time
}

type requestRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]rateBucket
}

const maximumRateLimitIdentities = 4096

func newRequestRateLimiter(rate, burst int) *requestRateLimiter {
	return &requestRateLimiter{rate: float64(rate), burst: float64(burst), buckets: make(map[string]rateBucket)}
}

func (l *requestRateLimiter) allow(key string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= maximumRateLimitIdentities {
			oldestKey, oldestTime := "", now
			for candidate, retained := range l.buckets {
				if oldestKey == "" || retained.last.Before(oldestTime) {
					oldestKey, oldestTime = candidate, retained.last
				}
			}
			delete(l.buckets, oldestKey)
		}
		bucket = rateBucket{tokens: l.burst, last: now}
	}
	bucket.tokens = min(l.burst, bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate)
	bucket.last = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		l.buckets[key] = bucket
		return true, 0
	}
	l.buckets[key] = bucket
	wait := int((1-bucket.tokens)/l.rate) + 1
	return false, max(1, wait)
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	if s.rateLimiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/v1/internal/") || r.URL.Path == "/v1/health" || r.URL.Path == "/v1/ready" {
			next.ServeHTTP(w, r)
			return
		}
		if allowed, retryAfter := s.rateLimiter.allow(rateLimitIdentity(r), time.Now()); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limit_exceeded", Message: "request rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rateLimitIdentity(r *http.Request) string {
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		digest := sha256.Sum256([]byte(authorization))
		return "credential:" + hex.EncodeToString(digest[:])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return "address:" + host
	}
	return "address:" + r.RemoteAddr
}
