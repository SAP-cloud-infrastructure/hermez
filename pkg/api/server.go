// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rs/cors"
	"github.com/spf13/viper"

	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/routing"
	"github.com/sapcc/hermes/pkg/storage"
)

// Server timeout defaults. net/http sets no timeouts by default, leaving the
// API open to slow-client (Slowloris) and idle-connection resource exhaustion.
//
// ReadHeaderTimeout bounds how long a client may take to send request headers,
// which defeats request-side Slowloris. ReadTimeout bounds the whole request
// read. WriteTimeout is 0 (unlimited) because legitimate audit queries and large
// event exports can be slow on the OpenSearch backend, and a response-side
// deadline would truncate them. The cost of WriteTimeout=0: a client that reads
// the response very slowly can hold a connection (and, if API.MaxConcurrentRequests
// is set, a concurrency slot) open. That response-side slow-read is bounded at
// the edge (LB/ingress idle timeout), not here.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

// Server sets up and starts the API server using httpapi patterns.
func Server(ctx context.Context, validator gopherpolicy.Validator, storageInterface storage.Storage, routingStore routing.Store, auditor audittools.Auditor) error {
	logg.Info("Starting Hermes API server")

	// Create API compositions
	v1API := NewV1API(validator, storageInterface, routingStore, auditor)
	versionAPI := NewVersionAPI(v1API.VersionData())
	metricsAPI := NewMetricsAPI()

	// Compose all APIs using httpapi
	handler := httpapi.Compose(
		v1API,
		versionAPI,
		metricsAPI,
	)

	// Apply middleware
	handler = InstrumentInflight(handler)

	// Bound the number of concurrent in-flight requests to protect the process
	// and the shared OpenSearch backend from a flood of expensive queries. A
	// value of 0 means "no limit"; production deployments should set a positive
	// value, with edge rate-limiting as the complementary control.
	maxConcurrent := viper.GetInt("API.MaxConcurrentRequests")
	handler = rejectOverCapacityMiddleware(maxConcurrent)(handler)

	// CORS. PUT and DELETE are required for the dataplane-config endpoints.
	//
	// Elektra (the OpenStack dashboard) calls this API directly from the browser
	// cross-origin, so CORS must permit those requests. A non-empty
	// API.CORSAllowedOrigins restricts access to those origins; an empty value
	// (the default) allows all origins (Access-Control-Allow-Origin: *).
	//
	// Allow-all is acceptable here because auth is carried in the X-Auth-Token
	// request header, not an ambient cookie: a browser never auto-attaches a
	// Keystone token to a cross-origin request, so a page cannot read audit data
	// without first obtaining a valid token. Set API.CORSAllowedOrigins to the
	// known dashboard origins to lock this down.
	corsOptions := cors.Options{
		AllowedHeaders: []string{"X-Auth-Token", "Content-Type", "Accept"},
		AllowedMethods: []string{"GET", "HEAD", "PUT", "DELETE"},
		MaxAge:         600,
	}
	if allowedOrigins := viper.GetStringSlice("API.CORSAllowedOrigins"); len(allowedOrigins) > 0 {
		corsOptions.AllowedOrigins = allowedOrigins
	}
	c := cors.New(corsOptions)
	handler = c.Handler(handler)

	// Start HTTP server. A custom http.Server carries the timeout defaults above.
	listenAddress := viper.GetString("API.ListenAddress")
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	// Graceful shutdown is driven by the caller-provided context, which main()
	// wires to SIGINT/SIGTERM.
	//
	// The goroutine selects on both ctx.Done() and serverExited: if ListenAndServe
	// returns early (e.g. a bind failure at startup), serverExited unblocks the
	// goroutine so it cannot leak. On shutdown, ctx.Done() fires first and
	// in-flight requests are drained.
	logg.Info("Listening on %s...", listenAddress)
	serverExited := make(chan struct{})
	shutdownDone := make(chan struct{})
	//nolint:gosec // G118: the shutdown goroutine intentionally uses a fresh context (not the already-cancelled ctx) so its deadline bounds the graceful-drain window.
	go func() {
		defer close(shutdownDone)
		select {
		case <-serverExited:
			// ListenAndServe already returned (startup/serve error); nothing to
			// drain, so exit to avoid leaking this goroutine.
			return
		case <-ctx.Done():
		}
		logg.Info("Shutting down HTTP server...")
		// A fresh context (not the already-cancelled ctx) governs how long
		// in-flight requests get to drain; deriving it from ctx would cancel the
		// drain immediately.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpext.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logg.Error("error during HTTP server shutdown: %s", err.Error())
		}
	}()

	err := server.ListenAndServe()
	close(serverExited)
	<-shutdownDone // wait for the goroutine to finish (drain or early-exit)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// rejectOverCapacityMiddleware rejects excess work instead of allowing an
// unbounded queue of goroutines to accumulate behind a concurrency limit.
func rejectOverCapacityMiddleware(maxRequests int) func(http.Handler) http.Handler {
	return func(inner http.Handler) http.Handler {
		if maxRequests <= 0 {
			return inner
		}

		semaphore := make(chan struct{}, maxRequests)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
				inner.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", "1")
				http.Error(w, "server is busy", http.StatusServiceUnavailable)
			}
		})
	}
}
