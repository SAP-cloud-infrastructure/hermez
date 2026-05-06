// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	policy "github.com/databus23/goslo.policy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/mock"

	"github.com/sapcc/hermes/pkg/storage"
	"github.com/sapcc/hermes/pkg/test"
)

func setupTest(t *testing.T) http.Handler {
	// load test policy (where everything is allowed)
	policyBytes, err := os.ReadFile("../test/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	policyRules := make(map[string]string)
	err = json.Unmarshal(policyBytes, &policyRules)
	if err != nil {
		t.Fatal(err)
	}
	policyEnforcer, err := policy.NewEnforcer(policyRules)
	if err != nil {
		t.Fatal(err)
	}
	viper.Set("hermes.PolicyEnforcer", policyEnforcer)

	// create test driver with the domains and projects from start-data.sql
	validator := mock.NewValidator(mock.NewEnforcer(), nil)
	storageInterface := storage.Mock{}

	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()

	// Create API compositions using httpapi
	v1API := NewV1API(validator, storageInterface)
	versionAPI := NewVersionAPI(v1API.VersionData())
	metricsAPI := NewMetricsAPI()

	// Compose all APIs using httpapi
	router := httpapi.Compose(
		v1API,
		versionAPI,
		metricsAPI,
	)

	return router
}

func Test_API(t *testing.T) {
	tt := []struct {
		name       string
		method     string
		path       string
		statuscode int
		json       string
	}{
		{"Metadata", "GET", "/v1/", http.StatusOK, "fixtures/api-metadata.json"},
		{"EventDetails", "GET", "/v1/events/7be6c4ff-b761-5f1f-b234-f5d41616c2cd", http.StatusOK, "fixtures/event-details.json"},
		{"EventList", "GET", "/v1/events?event_type=identity.project.deleted&offset=10", http.StatusOK, "fixtures/event-list.json"},
		{"Attributes", "GET", "/v1/attributes/resource_type", http.StatusOK, "fixtures/attributes.json"},
		{"InvalidEventID", "GET", "/v1/events/invalid-uuid", http.StatusBadRequest, ""},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			router := setupTest(t)

			test.APIRequest{
				Method:           tc.method,
				Path:             tc.path,
				ExpectStatusCode: tc.statuscode,
				ExpectJSON:       tc.json,
			}.Check(t, router)
		})
	}
}

func TestListEvents_ParameterParsing(t *testing.T) {
	validTimeStr := time.Now().UTC().Format(time.RFC3339)
	anotherValidTimeStr := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	tt := []struct {
		name             string
		path             string // Query string part
		expectStatusCode int
		// ExpectJSON will be "" for Bad Request (plain text error from http.Error)
		// For the one OK case, we won't specify ExpectJSON to avoid diffs with the mock.
		expectJSON string
	}{
		// --- Minimal "Loop Runs" Case (will get static data from mock) ---
		// This ensures the loop itself doesn't panic and can process a valid item.
		// We don't check the body because the mock returns static data.
		// TODO: Add a real test fixture for this.
		{"Sort_MinimalValidToRunLoop", "?sort=time", http.StatusOK, ""},

		// --- Sort Parameter Parsing Errors ---
		{"Sort_InvalidField", "?sort=invalidfield", http.StatusBadRequest, ""},
		{"Sort_InvalidDirection", "?sort=time:wrongdir", http.StatusBadRequest, ""},
		// Cases leading to empty sortElement or sortfield
		{"Sort_EmptyElementMiddle_FromCut", "?sort=time,,initiator_id", http.StatusBadRequest, ""},
		{"Sort_EmptyElementLeading_FromCut", "?sort=,time", http.StatusBadRequest, ""},
		{"Sort_OnlyCommas_FromCut", "?sort=,,", http.StatusBadRequest, ""},
		{"Sort_EmptyFieldNameExplicit", "?sort=:asc", http.StatusBadRequest, ""},

		// --- Time Parameter Parsing Errors ---
		// Minimal valid case for time loop
		{"Time_MinimalValidToRunLoop", "?time=lt:" + validTimeStr, http.StatusOK, ""},

		// Invalid cases
		{"Time_InvalidOperator", "?time=xx:" + validTimeStr, http.StatusBadRequest, ""},
		{"Time_DuplicateOperator", "?time=lt:" + validTimeStr + ",lt:" + anotherValidTimeStr, http.StatusBadRequest, ""},
		{"Time_MissingValue", "?time=lt:", http.StatusBadRequest, ""},
		{"Time_InvalidFormat", "?time=lt:not-a-time-format", http.StatusBadRequest, ""},
		// Cases leading to empty timeElement or operator
		{"Time_EmptyElementMiddle_FromCut", "?time=lt:" + validTimeStr + ",,gte:" + anotherValidTimeStr, http.StatusBadRequest, ""},
		{"Time_EmptyElementLeading_FromCut", "?time=,lt:" + validTimeStr, http.StatusBadRequest, ""},
		{"Time_OnlyCommas_FromCut", "?time=,,", http.StatusBadRequest, ""},
		{"Time_EmptyOperatorNameExplicit", "?time=:" + validTimeStr, http.StatusBadRequest, ""},
	}

	router := setupTest(t)

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			req := test.APIRequest{
				Method:           "GET",
				Path:             "/v1/events" + tc.path,
				ExpectStatusCode: tc.expectStatusCode,
			}
			// Only set ExpectJSON if we actually expect a specific JSON fixture (which we don't here for 200s)
			// For 400s, http.Error writes plain text, so ExpectJSON should be "" or nil.
			if tc.expectStatusCode != http.StatusOK {
				req.ExpectJSON = "" // For http.Error, response is not JSON
			}

			req.Check(t, router)
		})
	}
}
