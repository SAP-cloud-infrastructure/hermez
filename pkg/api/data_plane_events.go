// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/gorilla/mux"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/hermes/pkg/data_plane_events"
)

// DataPlaneEventsAPI exposes the per-project on/off toggle that controls
// whether data-plane events for a project are delivered to the project's
// immutable Ceph compliance bucket.
type DataPlaneEventsAPI struct {
	validator  gopherpolicy.Validator
	storage    data_plane_events.Storage
	auditor    audittools.Auditor
	auditSlots chan struct{}
}

// auditSlotCapacity bounds how many in-flight audit emits we tolerate before
// dropping. The go-bits Auditor.Record performs a synchronous send into a
// hard-coded 20-deep channel; under sustained RabbitMQ backpressure that
// blocks. Without a handler-side bound, every PATCH would spawn a goroutine
// that blocks indefinitely, growing memory until OOM.
//
// Cap 1024 is generous relative to expected toggle traffic (PATCH per
// project, rare): drops only happen under genuinely pathological backlog.
//
// TODO(remove-when-go-bits-storage-queue-lands): sapcc/go-bits PR #273
// (https://github.com/sapcc/go-bits/pull/273) adds a storage queue to the
// auditor itself. Once merged and adopted, this handler-side semaphore and
// its drop counter become redundant — delete this constant, the auditSlots
// field, the select in recordAudit, and the auditDropCounter.
const auditSlotCapacity = 1024

// NewDataPlaneEventsAPI constructs a DataPlaneEventsAPI.
func NewDataPlaneEventsAPI(validator gopherpolicy.Validator, storage data_plane_events.Storage, auditor audittools.Auditor) *DataPlaneEventsAPI {
	return &DataPlaneEventsAPI{
		validator:  validator,
		storage:    storage,
		auditor:    auditor,
		auditSlots: make(chan struct{}, auditSlotCapacity),
	}
}

// AddTo implements httpapi.API.
func (api *DataPlaneEventsAPI) AddTo(r *mux.Router) {
	r.Methods("GET").Path("/v1/projects/{project_id}/data-plane-events").Handler(
		InstrumentDuration("GetDataPlaneEvents")(InstrumentResponseSize("GetDataPlaneEvents")(http.HandlerFunc(api.handleGet))))
	r.Methods("PATCH").Path("/v1/projects/{project_id}/data-plane-events").Handler(
		InstrumentDuration("PatchDataPlaneEvents")(InstrumentResponseSize("PatchDataPlaneEvents")(http.HandlerFunc(api.handlePatch))))
}

// dataPlaneEventsResponse is the JSON shape returned by both GET and PATCH.
type dataPlaneEventsResponse struct {
	Enabled bool `json:"enabled"`
}

// dataPlaneEventsRequest is the JSON body accepted by PATCH. Unknown fields
// are rejected by the decoder; only "enabled" is allowed.
type dataPlaneEventsRequest struct {
	Enabled *bool `json:"enabled"`
}

// maxRequestBodyBytes caps PATCH bodies. The schema is one field, so 64 KiB
// is well above plausible legitimate input.
const maxRequestBodyBytes = 64 * 1024

// projectIDPattern enforces Keystone-style identifiers: alphanumeric, dashes,
// underscores; non-empty; 1..36 chars (matches the VARCHAR(36) column and
// Archer's convention; covers UUID-with-dashes). The literal "unavailable"
// is rejected separately, matching the Hermes storage convention.
var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,36}$`)

func validateProjectID(s string) error {
	if s == "" {
		return errors.New("project_id cannot be empty")
	}
	if s == "unavailable" {
		return errors.New("project_id 'unavailable' is not valid")
	}
	if !projectIDPattern.MatchString(s) {
		return errors.New("project_id must match [A-Za-z0-9_-]{1,36}")
	}
	return nil
}

func (api *DataPlaneEventsAPI) handleGet(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/projects/:project_id/data-plane-events")

	projectID, _, ok := api.authorize(w, r, "data_plane_events:show")
	if !ok {
		return
	}

	enabled, _, err := api.storage.Get(r.Context(), projectID)
	if err != nil {
		logg.Error("data_plane_events: storage.Get(%q): %s", projectID, err.Error())
		postgresErrorsCounter.Add(1)
		respondwith.ObfuscatedErrorText(w, err)
		return
	}
	ReturnESJSON(w, http.StatusOK, dataPlaneEventsResponse{Enabled: enabled})
}

func (api *DataPlaneEventsAPI) handlePatch(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/projects/:project_id/data-plane-events")

	// Authorize FIRST so an unauthenticated probe with a wrong Content-Type
	// gets a 401, not a 415 that would leak endpoint existence.
	projectID, token, ok := api.authorize(w, r, "data_plane_events:update")
	if !ok {
		return
	}

	// Parse Content-Type with mime.ParseMediaType so callers using
	// `application/json; charset=utf-8` (the default in JS fetch and
	// python-requests' json= mode) are not rejected.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, mtErr := mime.ParseMediaType(ct)
		if mtErr != nil || mediaType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
	} else {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Body cap. http.MaxBytesReader signals overflow via a typed error returned
	// by the JSON decoder.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body dataPlaneEventsRequest
	if err := dec.Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
			return
		}
		// Log full parser error (may include field names / offsets); send a
		// generic message to the client to avoid leaking schema detail.
		logg.Error("data_plane_events: JSON decode for project %q: %s", projectID, err.Error())
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	// Reject trailing JSON content (e.g. multiple objects).
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "request body must contain a single JSON object", http.StatusBadRequest)
		return
	}
	if body.Enabled == nil {
		http.Error(w, "request body must include 'enabled'", http.StatusBadRequest)
		return
	}

	changed, priorEnabled, err := api.storage.Set(r.Context(), projectID, *body.Enabled)
	if err != nil {
		logg.Error("data_plane_events: storage.Set(%q,%v): %s", projectID, *body.Enabled, err.Error())
		postgresErrorsCounter.Add(1)
		respondwith.ObfuscatedErrorText(w, err)
		return
	}

	if changed {
		// recordAudit happens BEFORE the response write: the audit reflects
		// the server's decision (storage transition committed), not the wire
		// outcome. ReasonCode=200 is therefore the intended/decided status.
		api.recordAudit(r, token, projectID, priorEnabled, *body.Enabled)
	}
	ReturnESJSON(w, http.StatusOK, dataPlaneEventsResponse{Enabled: *body.Enabled})
}

// authorize validates the project_id from the URL, applies the policy rule,
// and ensures the token's project scope matches the URL project_id.
// On success, it returns the validated project_id and the resolved token.
func (api *DataPlaneEventsAPI) authorize(w http.ResponseWriter, r *http.Request, rule string) (string, *gopherpolicy.Token, bool) {
	projectID := mux.Vars(r)["project_id"]
	if err := validateProjectID(projectID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil, false
	}

	token := api.validator.CheckToken(r)
	token.Context.Request = mux.Vars(r)
	token.Context.Request["project_id"] = projectID
	token.Context.Request["domain_id"] = token.Context.Auth["domain_id"]

	if !token.Require(w, rule) {
		return "", nil, false
	}

	// Customer scope: the token's project must match the URL project.
	// Cross-tenant (operator) access is out of scope for this PR. Fail-closed:
	// an empty/missing token project scope (e.g. domain-only token) does NOT
	// bypass the guard.
	if scoped := token.Context.Auth["project_id"]; scoped != projectID {
		http.Error(w, "forbidden: your token is scoped to a different project than the one in the URL", http.StatusForbidden)
		return "", nil, false
	}

	return projectID, token, true
}

// dataPlaneEventsTarget is the audittools.Target for the toggle resource.
type dataPlaneEventsTarget struct {
	ProjectID    string
	PriorEnabled bool
	NewEnabled   bool
}

// Render implements audittools.Target.
func (t dataPlaneEventsTarget) Render() cadf.Resource {
	res := cadf.Resource{
		TypeURI:   "service/audit/data_plane_events",
		ID:        t.ProjectID,
		ProjectID: t.ProjectID,
	}
	// must.Return: NewJSONAttachment marshals a map[string]bool literal which
	// cannot fail; treat the error as impossible. If it ever does, panic so
	// we surface a real bug rather than ship CADF events with empty payloads.
	att := must.Return(cadf.NewJSONAttachment("payload", map[string]bool{
		"prior_enabled": t.PriorEnabled,
		"new_enabled":   t.NewEnabled,
	}))
	res.Attachments = []cadf.Attachment{att}
	return res
}

// recordAudit emits a CADF event without blocking the HTTP request. The
// underlying audittools.Auditor.Record performs a synchronous send into the
// auditor's hard-coded 20-deep channel; under sustained RabbitMQ
// backpressure that send blocks indefinitely. We spawn a goroutine guarded
// by a 1024-slot semaphore: under pathological backlog we drop the event
// and increment a counter rather than let goroutines accumulate without
// bound and OOM the process.
//
// TODO(remove-when-go-bits-storage-queue-lands): sapcc/go-bits PR #273
// replaces the auditor's hard-coded channel with a storage queue. Once
// adopted, the select-with-default below collapses to `go api.auditor.Record(evt)`.
func (api *DataPlaneEventsAPI) recordAudit(r *http.Request, token *gopherpolicy.Token, projectID string, prior, next bool) {
	if api.auditor == nil || token == nil {
		return
	}
	action := cadf.EnableAction
	if !next {
		action = cadf.DisableAction
	}
	evt := audittools.Event{
		Time:       time.Now(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusOK,
		Action:     action,
		Target: dataPlaneEventsTarget{
			ProjectID:    projectID,
			PriorEnabled: prior,
			NewEnabled:   next,
		},
	}
	select {
	case api.auditSlots <- struct{}{}:
		go func() {
			defer func() {
				<-api.auditSlots
				if rec := recover(); rec != nil {
					logg.Error("data_plane_events: audit emit panicked: %v", rec)
				}
			}()
			api.auditor.Record(evt)
		}()
	default:
		// Saturated. Drop with a counter so operators can alert; dropping is
		// preferable to OOM-ing the process, but it IS event loss and must
		// page someone.
		auditDropCounter.Add(1)
		logg.Error("data_plane_events: audit emit dropped (slots saturated, cap=%d) for project %q action %q", auditSlotCapacity, projectID, action)
	}
}
