// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/hermes/pkg/routing"
)

// s3BucketNamePattern enforces RFC-1123-subset S3 bucket name rules:
// lowercase letters, digits, and hyphens; must start/end with letter or digit;
// 3–63 characters. Consecutive hyphens (e.g. "my--bucket") are rejected
// separately because they are prohibited by both AWS S3 and Ceph RGW.
var s3BucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$`)

// dataplaneConfigRequest is the shape accepted on PUT.
// We use strict decoding (DisallowUnknownFields) so unknown fields → 400.
type dataplaneConfigRequest struct {
	Enabled      bool   `json:"enabled"`
	TargetBucket string `json:"target_bucket"`
}

// GetDataplaneConfig handles GET /v1/projects/{project_id}/dataplane-config.
// Returns 200 with the default (disabled) payload when no config exists.
func (p *v1Provider) GetDataplaneConfig(res http.ResponseWriter, req *http.Request) {
	projectID := mux.Vars(req)["project_id"]
	if _, ok := p.authDataplaneConfig(res, req, projectID); !ok {
		return
	}

	cfg, err := p.routingStore.Get(req.Context(), projectID)
	if errors.Is(err, routing.ErrNotFound) {
		cfg = nil // treat as default
	} else if err != nil {
		logg.Error("dataplane-config GET: storage error for project %s: %s", projectID, err)
		respondwith.ObfuscatedErrorText(res, err)
		return
	}

	if cfg == nil {
		def := routing.DefaultDataplaneConfig(projectID)
		ReturnESJSON(res, http.StatusOK, def)
		return
	}
	ReturnESJSON(res, http.StatusOK, cfg)
}

// PutDataplaneConfig handles PUT /v1/projects/{project_id}/dataplane-config.
// Idempotent create-or-replace. Returns 200 with the saved document.
// An audit event is emitted for every attempt — successful or not.
func (p *v1Provider) PutDataplaneConfig(res http.ResponseWriter, req *http.Request) {
	projectID := mux.Vars(req)["project_id"]
	token, ok := p.authDataplaneConfig(res, req, projectID)
	if !ok {
		return
	}

	now := time.Now().UTC()
	userID := token.Context.Auth["user_id"]
	if userID == "" {
		http.Error(res, "token missing user identity", http.StatusUnauthorized)
		return
	}

	// recordAttempt emits a CADF event regardless of outcome.
	// cfg is nil when we can't construct the target (parse error before cfg is built).
	recordAttempt := func(reasonCode int, cfg *routing.DataplaneConfig) {
		target := routing.DataplaneConfig{ProjectID: projectID, UpdatedBy: userID}
		if cfg != nil {
			target = *cfg
		}
		p.auditor.Record(audittools.Event{
			Time:       now,
			Request:    req,
			User:       token,
			ReasonCode: reasonCode,
			Action:     cadf.UpdateAction,
			Target:     target,
		})
	}

	// Content-Type enforcement
	if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(res, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		recordAttempt(http.StatusUnsupportedMediaType, nil)
		return
	}

	// Body size cap: 64 KiB
	req.Body = http.MaxBytesReader(res, req.Body, 64*1024)

	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	var body dataplaneConfigRequest
	if err := decoder.Decode(&body); err != nil {
		http.Error(res, "invalid request body: "+err.Error(), http.StatusBadRequest)
		recordAttempt(http.StatusBadRequest, nil)
		return
	}

	// Validate target_bucket whenever it is non-empty — regardless of enabled flag.
	// This prevents storing an invalid bucket name that would silently break routing
	// if the config is later re-enabled without updating the bucket.
	if body.TargetBucket != "" {
		if !s3BucketNamePattern.MatchString(body.TargetBucket) {
			http.Error(res, "target_bucket must be 3–63 chars, lowercase letters/digits/hyphens only, start and end with letter or digit", http.StatusBadRequest)
			recordAttempt(http.StatusBadRequest, nil)
			return
		}
		if strings.Contains(body.TargetBucket, "--") {
			http.Error(res, "target_bucket must not contain consecutive hyphens", http.StatusBadRequest)
			recordAttempt(http.StatusBadRequest, nil)
			return
		}
	}

	// When enabled, a non-empty bucket is required.
	if body.Enabled && body.TargetBucket == "" {
		http.Error(res, "target_bucket is required when enabled is true", http.StatusBadRequest)
		recordAttempt(http.StatusBadRequest, nil)
		return
	}

	cfg := routing.DataplaneConfig{
		ProjectID:    projectID,
		Enabled:      body.Enabled,
		TargetBucket: body.TargetBucket,
		UpdatedAt:    now,
		UpdatedBy:    userID,
	}

	if err := p.routingStore.Upsert(req.Context(), cfg); err != nil {
		logg.Error("dataplane-config PUT: storage error for project %s: %s", projectID, err)
		respondwith.ObfuscatedErrorText(res, err)
		recordAttempt(http.StatusInternalServerError, &cfg)
		return
	}

	logg.Info("dataplane-config PUT: project=%s enabled=%v updated_by=%s", projectID, cfg.Enabled, userID)
	recordAttempt(http.StatusOK, &cfg)
	ReturnESJSON(res, http.StatusOK, cfg)
}

// DeleteDataplaneConfig handles DELETE /v1/projects/{project_id}/dataplane-config.
// Idempotent — deleting a non-existent config returns 204.
// An audit event is emitted only when a config actually existed and was removed;
// no-op deletes are silent (no spurious events for resources that never existed).
func (p *v1Provider) DeleteDataplaneConfig(res http.ResponseWriter, req *http.Request) {
	projectID := mux.Vars(req)["project_id"]
	token, ok := p.authDataplaneConfig(res, req, projectID)
	if !ok {
		return
	}

	now := time.Now().UTC()
	userID := token.Context.Auth["user_id"]
	if userID == "" {
		http.Error(res, "token missing user identity", http.StatusUnauthorized)
		return
	}

	deleted, err := p.routingStore.Delete(req.Context(), projectID)
	if err != nil {
		logg.Error("dataplane-config DELETE: storage error for project %s: %s", projectID, err)
		respondwith.ObfuscatedErrorText(res, err)
		// Emit failure event — the attempt was made even though storage failed.
		p.auditor.Record(audittools.Event{
			Time:       now,
			Request:    req,
			User:       token,
			ReasonCode: http.StatusInternalServerError,
			Action:     cadf.DeleteAction,
			Target:     routing.DataplaneConfig{ProjectID: projectID, UpdatedBy: userID},
		})
		return
	}

	// Only emit an audit event when something was actually removed.
	// A DELETE on a non-existent config is a safe no-op — recording it would
	// pollute the audit trail with spurious events for resources that never existed.
	if deleted {
		p.auditor.Record(audittools.Event{
			Time:       now,
			Request:    req,
			User:       token,
			ReasonCode: http.StatusNoContent,
			Action:     cadf.DeleteAction,
			Target:     routing.DataplaneConfig{ProjectID: projectID, UpdatedBy: userID},
		})
	}

	logg.Info("dataplane-config DELETE: project=%s updated_by=%s deleted=%v", projectID, userID, deleted)
	res.WriteHeader(http.StatusNoContent)
}

// authDataplaneConfig validates the Keystone token against the
// "dataplane_config:manage" policy rule and enforces that the path
// project_id matches the token's project scope.
//
// Returns the token and true on success; writes the error response and
// returns false on failure.
func (p *v1Provider) authDataplaneConfig(res http.ResponseWriter, req *http.Request, pathProjectID string) (*gopherpolicy.Token, bool) {
	token := p.validator.CheckToken(req)
	token.Context.Request = mux.Vars(req)
	token.Context.Request["domain_id"] = token.Context.Auth["domain_id"]
	token.Context.Request["project_id"] = token.Context.Auth["project_id"]

	if !token.Require(res, "dataplane_config:manage") {
		return nil, false
	}

	// Cross-project access check: path project_id must match the token scope.
	tokenProjectID := token.Context.Auth["project_id"]
	if tokenProjectID != pathProjectID {
		http.Error(res, "project_id in path does not match token scope", http.StatusForbidden)
		return nil, false
	}

	return token, true
}
