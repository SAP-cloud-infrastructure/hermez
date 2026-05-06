// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/hermes/pkg/storage"
)

// VersionData is used by version advertisement handlers.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []versionLinkData `json:"links"`
}

// versionLinkData is used by version advertisement handlers, as part of the
// VersionData struct.
type versionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

// v1Provider provides backward compatibility for existing handler methods
type v1Provider struct {
	validator gopherpolicy.Validator
	storage   storage.Storage
}

// AuthHandler wraps endpoint handlers with consistent auth logic.
func (p *v1Provider) AuthHandler(w http.ResponseWriter, r *http.Request, rule string) (*gopherpolicy.Token, bool) {
	token := p.validator.CheckToken(r)

	// Initialize request context with URL vars
	token.Context.Request = mux.Vars(r)

	// Handle domain_id with form value priority
	if formDomainID := r.FormValue("domain_id"); formDomainID != "" {
		token.Context.Request["domain_id"] = formDomainID
	} else {
		token.Context.Request["domain_id"] = token.Context.Auth["domain_id"]
	}

	// Handle project_id with form value priority
	if formProjectID := r.FormValue("project_id"); formProjectID != "" {
		token.Context.Request["project_id"] = formProjectID
	} else {
		token.Context.Request["project_id"] = token.Context.Auth["project_id"]
	}

	ok := token.Require(w, rule)
	return token, ok
}

// V1API implements the v1 API endpoints using httpapi patterns
type V1API struct {
	validator   gopherpolicy.Validator
	storage     storage.Storage
	versionData VersionData
	provider    *v1Provider
}

// NewV1API creates a new V1API instance with the provided validator and storage.
//
// Example:
//
//	validator := gopherpolicy.NewValidator(enforcer, logger)
//	storage := opensearch.NewStorage(config)
//	api := NewV1API(validator, storage)
func NewV1API(validator gopherpolicy.Validator, storageInterface storage.Storage) *V1API {
	api := &V1API{
		validator: validator,
		storage:   storageInterface,
		provider: &v1Provider{
			validator: validator,
			storage:   storageInterface,
		},
	}

	api.versionData = VersionData{
		Status: "CURRENT",
		ID:     "v1",
		Links: []versionLinkData{
			{
				Relation: "self",
				URL:      "/v1/",
			},
			{
				Relation: "describedby",
				URL:      "https://github.com/sapcc/hermes/tree/master/docs",
				Type:     "text/html",
			},
		},
	}

	return api
}

// VersionData returns the version data for this API
func (api *V1API) VersionData() VersionData {
	return api.versionData
}

// AddTo implements httpapi.API interface
func (api *V1API) AddTo(r *mux.Router) {
	r.Methods("GET").Path("/v1/").Handler(
		InstrumentDuration("version")(InstrumentResponseSize("version")(http.HandlerFunc(api.getVersion))))

	r.Methods("GET").Path("/v1/events").Handler(
		InstrumentDuration("ListEvents")(InstrumentResponseSize("ListEvents")(http.HandlerFunc(api.listEvents))))

	r.Methods("GET").Path("/v1/events/{event_id}").Handler(
		InstrumentDuration("GetEventDetails")(InstrumentResponseSize("GetEventDetails")(http.HandlerFunc(api.getEventDetails))))

	r.Methods("GET").Path("/v1/attributes/{attribute_name}").Handler(
		InstrumentDuration("GetAttributes")(InstrumentResponseSize("GetAttributes")(http.HandlerFunc(api.getAttributes))))
}

// Handler methods for V1API

// getVersion handles GET /v1/
func (api *V1API) getVersion(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1")

	// Update the self link with the actual request URL
	versionData := api.versionData
	versionData.Links[0].URL = fmt.Sprintf("%s://%s/v1/", getProtocol(r), r.Host)

	ReturnESJSON(w, http.StatusOK, map[string]any{"version": versionData})
}

// listEvents handles GET /v1/events
func (api *V1API) listEvents(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/events")

	// Call existing v1Provider implementation for backward compatibility
	api.provider.ListEvents(w, r)
}

// getEventDetails handles GET /v1/events/{event_id}
func (api *V1API) getEventDetails(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/events/:event_id")

	// Call existing v1Provider implementation for backward compatibility
	api.provider.GetEventDetails(w, r)
}

// getAttributes handles GET /v1/attributes/{attribute_name}
func (api *V1API) getAttributes(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/attributes/:attribute_name")

	// Call existing v1Provider implementation for backward compatibility
	api.provider.GetAttributes(w, r)
}
