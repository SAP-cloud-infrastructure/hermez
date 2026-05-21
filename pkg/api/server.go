// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/cors"
	"github.com/spf13/viper"

	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/data_plane_events"
	"github.com/sapcc/hermes/pkg/storage"
)

// methodNotAllowedAPI is a pseudo-API whose AddTo installs a
// MethodNotAllowedHandler on the shared *mux.Router. Registering it via
// httpapi.Compose keeps the handler wiring co-located with the router setup.
type methodNotAllowedAPI struct{}

// AddTo implements httpapi.API.
func (methodNotAllowedAPI) AddTo(r *mux.Router) {
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Hermes' public surface accepts only GET, HEAD, and PATCH (the
		// CORS-allowed set). A static Allow header is RFC-7231-§6.5.5
		// compliant for every registered route.
		w.Header().Set("Allow", "GET, HEAD, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

// Server Set up and start the API server using httpapi patterns
func Server(validator gopherpolicy.Validator, storageInterface storage.Storage, dpeStorage data_plane_events.Storage, auditor audittools.Auditor) error {
	logg.Info("Starting Hermes API server")

	// Create API compositions
	v1API := NewV1API(validator, storageInterface)
	versionAPI := NewVersionAPI(v1API.VersionData())
	metricsAPI := NewMetricsAPI()
	dpeAPI := NewDataPlaneEventsAPI(validator, dpeStorage, auditor)

	// Compose all APIs using httpapi.
	handler := httpapi.Compose(
		v1API,
		versionAPI,
		metricsAPI,
		dpeAPI,
		methodNotAllowedAPI{},
	)

	// Apply middleware
	handler = InstrumentInflight(handler)

	// Enable CORS support
	c := cors.New(cors.Options{
		AllowedHeaders: []string{"X-Auth-Token", "Content-Type", "Accept"},
		AllowedMethods: []string{"GET", "HEAD", "PATCH"},
		MaxAge:         600,
	})
	handler = c.Handler(handler)

	// Start HTTP server
	listenAddress := viper.GetString("API.ListenAddress")

	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)
	return httpext.ListenAndServeContext(ctx, listenAddress, handler)
}
