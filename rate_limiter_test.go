package gooctopi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTokenBucketLimiterWaitsWhenRateExceeded(t *testing.T) {
	t.Parallel()

	limiter := NewTokenBucketLimiter(2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := limiter.Wait(ctx); err != nil {
			t.Fatalf("wait failed: %v", err)
		}
	}
	elapsed := time.Since(start)

	minExpected := 450 * time.Millisecond
	if elapsed < minExpected {
		t.Fatalf("expected at least %v of throttling, got %v", minExpected, elapsed)
	}
}

func TestRateLimitedTransportInvokesLimiter(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{}
	transport := &RateLimitedTransport{
		Limiter: limiter,
		Base: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
				Request:    req,
			}, nil
		}),
	}

	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	if _, err := client.Do(req); err != nil {
		t.Fatalf("client request failed: %v", err)
	}

	if limiter.calls != 1 {
		t.Fatalf("expected limiter to be invoked once, got %d", limiter.calls)
	}
}

type stubLimiter struct {
	calls int
}

func (s *stubLimiter) Wait(ctx context.Context) error {
	s.calls++
	return ctx.Err()
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
