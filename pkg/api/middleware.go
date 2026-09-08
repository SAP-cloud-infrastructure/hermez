// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

// Prometheus metrics counters
var (
	authErrorsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hermes_logon_errors_count",
		Help: "Number of logon errors occurred",
	})
	authFailuresCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hermes_logon_failures_count",
		Help: "Number of logon attempts failed due to wrong credentials",
	})
	storageErrorsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hermes_storage_errors_count",
		Help: "Number of technical errors occurred when accessing underlying storage (i.e. OpenSearch)",
	})
	rateLimitExceededCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hermes_rate_limit_exceeded_total",
		Help: "Number of requests rejected by the rate limiter",
	}, []string{"handler"})

	// Metrics for handler instrumentation
	handlerMetrics    = make(map[string]*handlerMetricSet)
	handlerMetricsMux sync.RWMutex
)

// handlerMetricSet holds the metrics for a specific handler
type handlerMetricSet struct {
	durationHistogram     *prometheus.HistogramVec
	responseSizeHistogram *prometheus.HistogramVec
	once                  sync.Once
}

func init() {
	prometheus.MustRegister(authErrorsCounter, authFailuresCounter, storageErrorsCounter, rateLimitExceededCounter)
}

// InstrumentInflight wraps a handler with inflight request metrics
func InstrumentInflight(handler http.Handler) http.Handler {
	inflightGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hermes_requests_inflight",
		Help: "Number of inflight HTTP requests served by Hermes",
	})
	prometheus.MustRegister(inflightGauge)

	return promhttp.InstrumentHandlerInFlight(inflightGauge, handler)
}

// InstrumentDuration wraps a handler with request duration metrics
func InstrumentDuration(handlerName string) func(http.Handler) http.Handler {
	// Get or create metrics once when middleware is created, not on every request
	metrics := getOrCreateHandlerMetrics(handlerName)
	return func(next http.Handler) http.Handler {
		return promhttp.InstrumentHandlerDuration(metrics.durationHistogram, next)
	}
}

// InstrumentResponseSize wraps a handler with response size metrics
func InstrumentResponseSize(handlerName string) func(http.Handler) http.Handler {
	// Get or create metrics once when middleware is created, not on every request
	metrics := getOrCreateHandlerMetrics(handlerName)
	return func(next http.Handler) http.Handler {
		return promhttp.InstrumentHandlerResponseSize(metrics.responseSizeHistogram, next)
	}
}

// getOrCreateHandlerMetrics safely gets or creates metrics for a handler
func getOrCreateHandlerMetrics(handlerName string) *handlerMetricSet {
	handlerMetricsMux.RLock()
	metrics, exists := handlerMetrics[handlerName]
	handlerMetricsMux.RUnlock()

	if exists {
		return metrics
	}

	handlerMetricsMux.Lock()
	defer handlerMetricsMux.Unlock()

	// Double-check in case another goroutine created it
	if existingMetrics, exists := handlerMetrics[handlerName]; exists {
		return existingMetrics
	}

	// Create new metrics for this handler
	metrics = &handlerMetricSet{}
	metrics.once.Do(func() {
		metrics.durationHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:        "hermes_request_duration_seconds",
				Help:        "Duration/latency of a Hermes request",
				ConstLabels: prometheus.Labels{"handler": handlerName},
				Buckets:     prometheus.DefBuckets,
			},
			[]string{},
		)
		metrics.responseSizeHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:        "hermes_response_size_bytes",
				Help:        "Size of the Hermes response (e.g. to a query)",
				ConstLabels: prometheus.Labels{"handler": handlerName},
				Buckets:     prometheus.LinearBuckets(100, 100, 10),
			},
			[]string{},
		)
		prometheus.MustRegister(metrics.durationHistogram, metrics.responseSizeHistogram)
	})

	handlerMetrics[handlerName] = metrics
	return metrics
}

// perKeyLimiter pairs a rate limiter with the last time it was used.
type perKeyLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitMiddleware is a per-key token rate limiter keyed by the
// X-Auth-Token header value. A nil value means rate limiting is disabled.
type RateLimitMiddleware struct {
	rps          rate.Limit
	burst        int
	handlerLabel string
	mu           sync.Mutex
	limiters     map[string]*perKeyLimiter
}

// NewRateLimitMiddleware returns a new RateLimitMiddleware or nil when disabled
// (rps <= 0 or burst <= 0).
func NewRateLimitMiddleware(rps float64, burst int, handlerLabel string) *RateLimitMiddleware {
	if rps <= 0 || burst <= 0 {
		return nil
	}
	return &RateLimitMiddleware{
		rps:          rate.Limit(rps),
		burst:        burst,
		handlerLabel: handlerLabel,
		limiters:     make(map[string]*perKeyLimiter),
	}
}

// StartEviction starts a background goroutine that removes limiters not seen
// within maxIdle, checking every interval. The goroutine exits when ctx is done.
func (rl *RateLimitMiddleware) StartEviction(ctx context.Context, interval, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.evictStale(maxIdle)
			}
		}
	}()
}

func (rl *RateLimitMiddleware) evictStale(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, entry := range rl.limiters {
		if entry.lastSeen.Before(cutoff) {
			delete(rl.limiters, key)
		}
	}
}

// getLimiter returns the limiter for key, creating one if necessary.
func (rl *RateLimitMiddleware) getLimiter(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, ok := rl.limiters[key]
	if !ok {
		entry = &perKeyLimiter{limiter: rate.NewLimiter(rl.rps, rl.burst)}
		rl.limiters[key] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// Wrap wraps next with rate limiting. Requests are keyed by the X-Auth-Token
// header (one burst limiter per token / authenticated session). Requests that exceed
// the limit receive HTTP 429 with a Retry-After header.
func (rl *RateLimitMiddleware) Wrap(next http.Handler) http.Handler {
	if rl == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Auth-Token")
		if key == "" {
			key = r.RemoteAddr
		}
		if !rl.getLimiter(key).Allow() {
			retryAfter := int(1/float64(rl.rps)) + 1
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			rateLimitExceededCounter.WithLabelValues(rl.handlerLabel).Inc()
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
