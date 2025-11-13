package gooctopi

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Limiter is a minimal interface implemented by rate limiters.
type Limiter interface {
	Wait(ctx context.Context) error
}

// TokenBucketLimiter implements a simple token bucket rate limiter.
type TokenBucketLimiter struct {
	mu       sync.Mutex
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
}

// NewTokenBucketLimiter constructs a limiter that issues up to rate tokens per second
// with the provided burst capacity.
func NewTokenBucketLimiter(rate float64, burst int) *TokenBucketLimiter {
	if rate <= 0 {
		panic("rate must be positive")
	}
	if burst <= 0 {
		panic("burst must be positive")
	}
	return &TokenBucketLimiter{
		rate:     rate,
		capacity: float64(burst),
		tokens:   float64(burst),
		last:     time.Now(),
	}
}

// Wait blocks until a single token is available or the context is cancelled.
func (l *TokenBucketLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		now := time.Now()
		l.refill(now)

		if l.tokens >= 1 {
			l.tokens--
			return nil
		}

		needed := (1 - l.tokens) / l.rate
		waitDuration := time.Duration(needed * float64(time.Second))
		if waitDuration <= 0 {
			waitDuration = time.Millisecond
		}

		timer := time.NewTimer(waitDuration)
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		l.mu.Lock()
	}
}

func (l *TokenBucketLimiter) refill(now time.Time) {
	elapsed := now.Sub(l.last)
	if elapsed <= 0 {
		return
	}
	l.tokens += l.rate * elapsed.Seconds()
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.last = now
}

// RateLimitedTransport wraps a RoundTripper with a limiter.
type RateLimitedTransport struct {
	Limiter Limiter
	Base    http.RoundTripper
}

// RoundTrip waits for the limiter before delegating to the base transport.
func (t *RateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Limiter != nil {
		if err := t.Limiter.Wait(req.Context()); err != nil {
			return nil, err
		}
	}
	return t.base().RoundTrip(req)
}

func (t *RateLimitedTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

type rateLimitConfig struct {
	rate  float64
	burst int
}

var (
	defaultLimiterRegistry = newLimiterRegistry()
	rateLimitConfigs       = map[string]rateLimitConfig{
		solanaMainnetHost: {rate: 9, burst: 9},
	}
)

type limiterRegistry struct {
	mu       sync.Mutex
	limiters map[string]Limiter
}

func newLimiterRegistry() *limiterRegistry {
	return &limiterRegistry{
		limiters: make(map[string]Limiter),
	}
}

func (r *limiterRegistry) get(key string, factory func() Limiter) Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limiter, ok := r.limiters[key]; ok {
		return limiter
	}
	limiter := factory()
	if limiter != nil {
		r.limiters[key] = limiter
	}
	return limiter
}

func limiterForEndpoint(endpoint string) Limiter {
	host := hostFromEndpoint(endpoint)
	if host == "" {
		return nil
	}
	cfg, ok := rateLimitConfigs[host]
	if !ok {
		return nil
	}
	return defaultLimiterRegistry.get(host, func() Limiter {
		return NewTokenBucketLimiter(cfg.rate, cfg.burst)
	})
}

func hostFromEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
