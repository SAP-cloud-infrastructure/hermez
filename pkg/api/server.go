// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"

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

// Server Set up and start the API server using httpapi patterns
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

	// Enable CORS support — PUT and DELETE are required for the dataplane-config endpoints.
	c := cors.New(cors.Options{
		AllowedHeaders: []string{"X-Auth-Token", "Content-Type", "Accept"},
		AllowedMethods: []string{"GET", "HEAD", "PUT", "DELETE"},
		MaxAge:         600,
	})
	handler = c.Handler(handler)

	// Start HTTP server
	listenAddress := viper.GetString("API.ListenAddress")

	return httpext.ListenAndServeContext(ctx, listenAddress, handler)
}
