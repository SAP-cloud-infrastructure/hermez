// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func makeOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func requestWithToken(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("X-Auth-Token", token)
	}
	return r
}

func counterValue(t *testing.T, reg *prometheus.Registry, name, handlerLabel string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "handler" && lp.GetValue() == handlerLabel {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// simpleRateLimitSetup creates a RateLimitMiddleware with a fresh counter registered
// on a new pedantic registry. Returns the middleware and the registry for asserting counter values.
func simpleRateLimitSetup(t *testing.T, rps float64, burst int, label string) (*RateLimitMiddleware, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	prometheus.DefaultRegisterer = reg

	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hermes_rate_limit_exceeded_total",
		Help: "Number of requests rejected by the rate limiter",
	}, []string{"handler"})
	reg.MustRegister(cv)
	rateLimitExceededCounter = cv

	rl := NewRateLimitMiddleware(rps, burst, label)
	return rl, reg
}

func TestRateLimitMiddleware_AllowUnderLimit(t *testing.T) {
	rl, _ := simpleRateLimitSetup(t, 100, 100, "test")
	handler := rl.Wrap(makeOKHandler())

	for i := range 5 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestWithToken("token-a"))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}
}

func TestRateLimitMiddleware_RejectOverLimit(t *testing.T) {
	rl, reg := simpleRateLimitSetup(t, 1, 1, "test")
	handler := rl.Wrap(makeOKHandler())

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, requestWithToken("token-b"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, requestWithToken("token-b"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Fatal("second request: missing Retry-After header")
	}

	// Prometheus counter should be 1.
	got := counterValue(t, reg, "hermes_rate_limit_exceeded_total", "test")
	if got != 1 {
		t.Fatalf("counter: expected 1, got %v", got)
	}
}

func TestRateLimitMiddleware_DifferentTokensIndependent(t *testing.T) {
	rl, _ := simpleRateLimitSetup(t, 1, 1, "test")
	handler := rl.Wrap(makeOKHandler())

	// Each distinct token gets its own burst limiter; all first requests should succeed.
	for _, tok := range []string{"alpha", "beta", "gamma"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestWithToken(tok))
		if rec.Code != http.StatusOK {
			t.Fatalf("token %s: expected 200, got %d", tok, rec.Code)
		}
	}
}

func TestRateLimitMiddleware_FallbackToRemoteAddr(t *testing.T) {
	rl, _ := simpleRateLimitSetup(t, 1, 1, "test")
	handler := rl.Wrap(makeOKHandler())

	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = "1.2.3.4:1234"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, r1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec1.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "1.2.3.4:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rec2.Code)
	}
}

func TestRateLimitMiddleware_NilDisabled(t *testing.T) {
	var rl *RateLimitMiddleware
	handler := rl.Wrap(makeOKHandler())

	for range 10 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestWithToken("any"))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func TestNewRateLimitMiddleware_ZeroDisables(t *testing.T) {
	if NewRateLimitMiddleware(0, 10, "x") != nil {
		t.Fatal("expected nil when rps=0")
	}
	if NewRateLimitMiddleware(10, 0, "x") != nil {
		t.Fatal("expected nil when burst=0")
	}
	if NewRateLimitMiddleware(0, 0, "x") != nil {
		t.Fatal("expected nil when both zero")
	}
}

func TestRateLimitMiddleware_Eviction(t *testing.T) {
	rl, _ := simpleRateLimitSetup(t, 100, 100, "test")

	handler := rl.Wrap(makeOKHandler())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithToken("evict-me"))

	// Backdate lastSeen so the entry looks stale.
	rl.mu.Lock()
	rl.limiters["evict-me"].lastSeen = time.Now().Add(-20 * time.Minute)
	rl.mu.Unlock()

	rl.evictStale(10 * time.Minute)

	rl.mu.Lock()
	_, exists := rl.limiters["evict-me"]
	rl.mu.Unlock()

	if exists {
		t.Fatal("expected stale limiter to be evicted")
	}
}

func TestRateLimitMiddleware_ActiveNotEvicted(t *testing.T) {
	rl, _ := simpleRateLimitSetup(t, 100, 100, "test")

	handler := rl.Wrap(makeOKHandler())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestWithToken("keep-me"))

	// lastSeen is recent; should survive eviction.
	rl.evictStale(10 * time.Minute)

	rl.mu.Lock()
	_, exists := rl.limiters["keep-me"]
	rl.mu.Unlock()

	if !exists {
		t.Fatal("expected active limiter to survive eviction")
	}
}

func TestRateLimitMiddleware_PrometheusCounter(t *testing.T) {
	rl, reg := simpleRateLimitSetup(t, 1, 1, "myhandler")
	handler := rl.Wrap(makeOKHandler())

	// Exhaust the burst then make more requests that should be rejected.
	handler.ServeHTTP(httptest.NewRecorder(), requestWithToken("tok"))
	handler.ServeHTTP(httptest.NewRecorder(), requestWithToken("tok"))
	handler.ServeHTTP(httptest.NewRecorder(), requestWithToken("tok"))

	got := counterValue(t, reg, "hermes_rate_limit_exceeded_total", "myhandler")
	if got < 1 {
		t.Fatalf("expected at least 1 rejection, counter=%v", got)
	}
}
