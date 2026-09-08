// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sapcc/go-bits/httpapi"
)

// VersionAPI handles version advertisement
type VersionAPI struct {
	v1VersionData VersionData
}

// NewVersionAPI creates a version API instance
func NewVersionAPI(v1VersionData VersionData) *VersionAPI {
	return &VersionAPI{v1VersionData: v1VersionData}
}

// AddTo implements httpapi.API interface
func (api *VersionAPI) AddTo(c *httpapi.Composer) {
	c.Router().Methods("GET").Path("/").HandlerFunc(api.listVersions)
}

func (api *VersionAPI) listVersions(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/")

	allVersions := struct {
		Versions []VersionData `json:"versions"`
	}{[]VersionData{api.v1VersionData}}

	ReturnESJSON(w, http.StatusMultipleChoices, allVersions)
}

// MetricsAPI handles Prometheus metrics
type MetricsAPI struct{}

// NewMetricsAPI creates a metrics API instance
func NewMetricsAPI() *MetricsAPI {
	return &MetricsAPI{}
}

// AddTo implements httpapi.API interface
func (api *MetricsAPI) AddTo(c *httpapi.Composer) {
	c.Router().Methods("GET").Path("/metrics").Handler(promhttp.Handler())
}
