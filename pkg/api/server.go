// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"time"

	"github.com/rs/cors"
	"github.com/spf13/viper"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/storage"
)

// Server Set up and start the API server using httpapi patterns
func Server(validator gopherpolicy.Validator, storageInterface storage.Storage) error {
	logg.Info("Starting Hermes API server")

	// Create API compositions
	v1API := NewV1API(validator, storageInterface)
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

	// Enable CORS support
	c := cors.New(cors.Options{
		AllowedHeaders: []string{"X-Auth-Token", "Content-Type", "Accept"},
		AllowedMethods: []string{"GET", "HEAD"},
		MaxAge:         600,
	})
	handler = c.Handler(handler)

	// Start HTTP server
	listenAddress := viper.GetString("API.ListenAddress")

	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)
	return httpext.ListenAndServeContext(ctx, listenAddress, handler)
}
