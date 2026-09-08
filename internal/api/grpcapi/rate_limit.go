package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const maxLimiterIdentities = 4096

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

func newRequestRateLimiter(rate, burst int) *requestRateLimiter {
	if rate <= 0 || burst <= 0 {
		return nil
	}
	return &requestRateLimiter{rate: float64(rate), burst: float64(burst), buckets: make(map[string]rateBucket)}
}

func (l *requestRateLimiter) allow(identity string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.buckets[identity]; !exists && len(l.buckets) >= maxLimiterIdentities {
		oldestIdentity := ""
		var oldest time.Time
		for candidate, bucket := range l.buckets {
			if oldestIdentity == "" || bucket.last.Before(oldest) {
				oldestIdentity, oldest = candidate, bucket.last
			}
		}
		delete(l.buckets, oldestIdentity)
	}
	bucket, exists := l.buckets[identity]
	if !exists {
		bucket = rateBucket{tokens: l.burst, last: now}
	} else {
		bucket.tokens = min(l.burst, bucket.tokens+now.Sub(bucket.last).Seconds()*l.rate)
		bucket.last = now
	}
	if bucket.tokens >= 1 {
		bucket.tokens--
		l.buckets[identity] = bucket
		return true, 0
	}
	retry := time.Duration((1 - bucket.tokens) / l.rate * float64(time.Second))
	if retry < time.Millisecond {
		retry = time.Millisecond
	}
	l.buckets[identity] = bucket
	return false, retry
}

func rateLimitUnaryInterceptor(limiter *requestRateLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if allowed, retry := limiter.allow(rateLimitIdentity(ctx), time.Now()); !allowed {
			_ = grpc.SetHeader(ctx, retryMetadata(retry))
			return nil, status.Error(codes.ResourceExhausted, "request rate limit exceeded")
		}
		return handler(ctx, request)
	}
}

func rateLimitStreamInterceptor(limiter *requestRateLimiter) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if allowed, retry := limiter.allow(rateLimitIdentity(stream.Context()), time.Now()); !allowed {
			_ = stream.SetHeader(retryMetadata(retry))
			return status.Error(codes.ResourceExhausted, "request rate limit exceeded")
		}
		return handler(server, stream)
	}
}

func rateLimitIdentity(ctx context.Context) string {
	if principal, _ := ctx.Value(principalContextKey{}).(*Principal); principal != nil {
		return "principal:" + principal.Name
	}
	if values := metadata.ValueFromIncomingContext(ctx, "authorization"); len(values) == 1 {
		digest := sha256.Sum256([]byte(values[0]))
		return "credential:" + hex.EncodeToString(digest[:])
	}
	if caller, ok := peer.FromContext(ctx); ok && caller.Addr != nil {
		return "peer:" + caller.Addr.String()
	}
	return "peer:unknown"
}

func retryMetadata(retry time.Duration) metadata.MD {
	milliseconds := (retry + time.Millisecond - 1) / time.Millisecond
	return metadata.Pairs("retry-after-ms", fmt.Sprintf("%d", milliseconds))
}
