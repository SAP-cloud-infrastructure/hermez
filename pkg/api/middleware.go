// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
		Help: "Number of technical errors occurred when accessing underlying storage (i.e. Elasticsearch)",
	})
	// Detailed storage error metrics
	storageErrorsCounterVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hermes_storage_errors_by_type_count",
			Help: "Number of storage errors by error type",
		},
		[]string{"error_code"},
	)

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
	prometheus.MustRegister(authErrorsCounter, authFailuresCounter, storageErrorsCounter, storageErrorsCounterVec)
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
