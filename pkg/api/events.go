// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/gorilla/mux"

	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/hermes/pkg/hermes"
	"github.com/sapcc/hermes/pkg/storage"
)

// EventList is the model for JSON returned by the ListEvents API call
type EventList struct {
	NextURL string              `json:"next,omitempty"`
	PrevURL string              `json:"previous,omitempty"`
	Events  []*hermes.ListEvent `json:"events"`
	Total   int                 `json:"total"`
}

var validSortTopics = map[string]bool{
	"time":           true,
	"initiator_id":   true,
	"observer_type":  true,
	"target_type":    true,
	"target_id":      true,
	"action":         true,
	"outcome":        true,
	"initiator_name": true,
	"initiator_type": true,
	"request_path":   true,
	// deprecated
	"source":        true,
	"resource_type": true,
	"resource_name": true,
	"event_type":    true,
}

var validSortDirections = map[string]bool{"asc": true, "desc": true}

var validTimeOperators = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true}

var validTimeFormats = []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05"}

// parseSortParam parses the "sort" query parameter into a slice of FieldOrder.
// Returns (nil, nil) when the parameter is empty.
// Writes a 400 response and returns a non-nil error on invalid input.
func parseSortParam(res http.ResponseWriter, req *http.Request) ([]hermes.FieldOrder, error) {
	sortParam := req.FormValue("sort")
	var sortSpec []hermes.FieldOrder

	for sortElement := range strings.SplitSeq(sortParam, ",") {
		sortElement = strings.TrimSpace(sortElement)
		if sortElement == "" {
			if strings.TrimSpace(sortParam) != "" {
				http.Error(res, "Invalid sort parameter", http.StatusBadRequest)
				return nil, errors.New("invalid sort parameter")
			}
			continue
		}

		sortfield, direction, foundColon := strings.Cut(sortElement, ":")
		if sortfield == "" {
			http.Error(res, "Invalid sort parameter: field name cannot be empty", http.StatusBadRequest)
			return nil, errors.New("invalid sort parameter")
		}
		if !validSortTopics[sortfield] {
			msg := fmt.Sprintf("not a valid topic: %s, valid topics: %v", sortfield, reflect.ValueOf(validSortTopics).MapKeys())
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}

		order := "asc"
		if foundColon {
			sortDirection := strings.TrimSpace(direction)
			if sortDirection == "" {
				msg := fmt.Sprintf("sort direction for field %s cannot be empty", sortfield)
				http.Error(res, msg, http.StatusBadRequest)
				return nil, errors.New(msg)
			}
			if !validSortDirections[sortDirection] {
				msg := fmt.Sprintf("sort direction %s is invalid, must be asc or desc", sortDirection)
				http.Error(res, msg, http.StatusBadRequest)
				return nil, errors.New(msg)
			}
			order = sortDirection
		}
		sortSpec = append(sortSpec, hermes.FieldOrder{Fieldname: sortfield, Order: order})
	}
	return sortSpec, nil
}

// parseTimeParam parses the "time" query parameter into an operator→timestamp map.
// Returns (nil, nil) when the parameter is empty.
// Writes a 400 response and returns a non-nil error on invalid input.
func parseTimeParam(res http.ResponseWriter, req *http.Request) (map[string]string, error) {
	timeParam := req.FormValue("time")
	timeRange := make(map[string]string)

	for timeElement := range strings.SplitSeq(timeParam, ",") {
		timeElement = strings.TrimSpace(timeElement)
		if timeElement == "" {
			if strings.TrimSpace(timeParam) != "" {
				http.Error(res, "Invalid time parameter: an element is empty", http.StatusBadRequest)
				return nil, errors.New("invalid time parameter")
			}
			continue
		}

		operator, value, foundColon := strings.Cut(timeElement, ":")
		if operator == "" {
			http.Error(res, "Invalid time parameter: operator cannot be empty", http.StatusBadRequest)
			return nil, errors.New("invalid time parameter")
		}
		if !validTimeOperators[operator] {
			msg := fmt.Sprintf("time operator %s is not valid. Must be lt, lte, gt or gte", operator)
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}
		if !foundColon {
			msg := fmt.Sprintf("time operator %s missing :<timestamp>", operator)
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}
		timeStr := strings.TrimSpace(value)
		if timeStr == "" {
			msg := fmt.Sprintf("time operator %s missing :<timestamp>", operator)
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}
		if _, exists := timeRange[operator]; exists {
			msg := fmt.Sprintf("time operator %s can only occur once", operator)
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}
		var valid bool
		for _, tf := range validTimeFormats {
			if _, err := time.Parse(tf, timeStr); err == nil {
				valid = true
				break
			}
		}
		if !valid {
			msg := "invalid time format: " + timeStr
			http.Error(res, msg, http.StatusBadRequest)
			return nil, errors.New(msg)
		}
		timeRange[operator] = timeStr
	}
	return timeRange, nil
}

// buildEventFilter constructs an EventFilter from request query parameters.
// sortSpec and timeRange should come from parseSortParam/parseTimeParam.
// offset and limit are caller-supplied (differ between List and Download).
func buildEventFilter(req *http.Request, sortSpec []hermes.FieldOrder, timeRange map[string]string, offset, limit uint) hermes.EventFilter {
	return hermes.EventFilter{
		ObserverType:  req.FormValue("observer_type") + req.FormValue("source"),
		TargetType:    req.FormValue("target_type") + req.FormValue("resource_type"),
		TargetID:      req.FormValue("target_id"),
		InitiatorID:   req.FormValue("initiator_id") + req.FormValue("user_name"),
		InitiatorType: req.FormValue("initiator_type"),
		InitiatorName: req.FormValue("initiator_name"),
		Action:        req.FormValue("action") + req.FormValue("event_type"),
		Outcome:       req.FormValue("outcome"),
		Search:        req.FormValue("search"),
		RequestPath:   req.FormValue("request_path"),
		Time:          timeRange,
		Offset:        offset,
		Limit:         limit,
		Sort:          sortSpec,
		Details:       req.Form.Has("details"),
	}
}

// ListEvents handles GET /v1/events.
func (p *v1Provider) ListEvents(res http.ResponseWriter, req *http.Request) {
	logg.Debug("* api.ListEvents: Check token")
	token, ok := p.AuthHandler(res, req, "event:list")
	if !ok {
		return
	}

	// Parse offset and limit
	var offset, limit uint = 0, 10
	if offsetStr := req.FormValue("offset"); offsetStr != "" {
		parsed, err := strconv.ParseUint(offsetStr, 10, 32)
		if err != nil {
			http.Error(res, "Invalid offset value", http.StatusBadRequest)
			return
		}
		if parsed > math.MaxInt32 {
			http.Error(res, fmt.Sprintf("Offset must be less than or equal to %d", math.MaxInt32), http.StatusBadRequest)
			return
		}
		offset = uint(parsed)
	}
	if limitStr := req.FormValue("limit"); limitStr != "" {
		parsed, err := strconv.ParseUint(limitStr, 10, 32)
		if err != nil {
			http.Error(res, "Invalid limit value", http.StatusBadRequest)
			return
		}
		if parsed > math.MaxInt32 {
			http.Error(res, fmt.Sprintf("Limit must be less than or equal to %d", math.MaxInt32), http.StatusBadRequest)
			return
		}
		limit = uint(parsed)
	}

	sortSpec, err := parseSortParam(res, req)
	if err != nil {
		return
	}
	timeRange, err := parseTimeParam(res, req)
	if err != nil {
		return
	}

	logg.Debug("api.ListEvents: Create filter")
	filter := buildEventFilter(req, sortSpec, timeRange, offset, limit)

	logg.Debug("api.ListEvents: call hermes.GetEvents()")
	indexID, err := getIndexID(token, req, res)
	if err != nil {
		return
	}
	events, total, err := hermes.GetEvents(req.Context(), &filter, indexID, p.storage)
	if respondwith.ErrorText(res, err) {
		logg.Error("api.ListEvents: error calling hermes.GetEvents(): %s", err.Error())
		if unmarshalErr, ok := errext.As[*json.UnmarshalTypeError](err); ok {
			logg.Error("api.ListEvents: JSON unmarshal error: Type=%v, Value=%v, Offset=%v, Struct=%v, Field=%v",
				unmarshalErr.Type, unmarshalErr.Value, unmarshalErr.Offset, unmarshalErr.Struct, unmarshalErr.Field)
		}
		storageErrorsCounter.Add(1)
		return
	}

	eventList := EventList{Events: events, Total: total}
	protocol := getProtocol(req)

	if total >= 0 && filter.Offset+filter.Limit < uint(total) {
		req.Form.Set("offset", strconv.FormatUint(uint64(filter.Offset+filter.Limit), 10))
		eventList.NextURL = fmt.Sprintf("%s://%s%s?%s", protocol, req.Host, req.URL.Path, req.Form.Encode())
	}
	if filter.Offset >= filter.Limit {
		req.Form.Set("offset", strconv.FormatUint(uint64(filter.Offset-filter.Limit), 10))
		eventList.PrevURL = fmt.Sprintf("%s://%s%s?%s", protocol, req.Host, req.URL.Path, req.Form.Encode())
	}

	ReturnESJSON(res, http.StatusOK, eventList)
}

// DownloadEvents handles GET /v1/events/download.
// Accepts the same filter parameters as ListEvents but streams all matching
// events as newline-delimited JSON (JSONL) without pagination limits.
func (p *v1Provider) DownloadEvents(res http.ResponseWriter, req *http.Request) {
	logg.Debug("* api.DownloadEvents: Check token")
	token, ok := p.AuthHandler(res, req, "event:list")
	if !ok {
		return
	}

	sortSpec, err := parseSortParam(res, req)
	if err != nil {
		return
	}
	timeRange, err := parseTimeParam(res, req)
	if err != nil {
		return
	}

	indexID, err := getIndexID(token, req, res)
	if err != nil {
		return
	}

	pageSize := p.storage.MaxLimit()
	if pageSize == 0 {
		pageSize = 1000
	}

	res.Header().Set("Content-Type", "application/x-ndjson")
	res.Header().Set("Content-Disposition", `attachment; filename="audit-events.jsonl"`)

	enc := json.NewEncoder(res)
	var offset uint
	for {
		filter := buildEventFilter(req, sortSpec, timeRange, offset, pageSize)

		logg.Debug("api.DownloadEvents: fetching offset=%d limit=%d", offset, pageSize)
		events, total, err := hermes.GetEvents(req.Context(), &filter, indexID, p.storage)
		if err != nil {
			logg.Error("api.DownloadEvents: error calling hermes.GetEvents(): %s", err.Error())
			storageErrorsCounter.Add(1)
			return
		}

		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				logg.Error("api.DownloadEvents: error encoding event: %s", err.Error())
				return
			}
		}

		offset += uint(len(events))
		if offset >= uint(total) || len(events) == 0 {
			break
		}
	}
}

// GetEventDetails handles GET /v1/events/:event_id.
func (p *v1Provider) GetEventDetails(res http.ResponseWriter, req *http.Request) {
	token, ok := p.AuthHandler(res, req, "event:show")
	if !ok {
		return
	}

	// Sanitize user input
	eventID := mux.Vars(req)["event_id"]
	eventID = strings.ReplaceAll(eventID, "\n", "")
	eventID = strings.ReplaceAll(eventID, "\r", "")

	// Validate if eventID is a valid UUID
	if _, err := uuid.Parse(eventID); err != nil {
		http.Error(res, "Invalid event ID format", http.StatusBadRequest)
		return
	}

	indexID, err := getIndexID(token, req, res)
	if err != nil {
		return
	}

	event, err := hermes.GetEvent(req.Context(), eventID, indexID, p.storage)

	if respondwith.ErrorText(res, err) {
		logg.Error("error getting events from Storage: %s", err)
		storageErrorsCounter.Add(1)
		return
	}
	if event == nil {
		err := fmt.Errorf("event %s could not be found in project %s", eventID, indexID)
		http.Error(res, err.Error(), http.StatusNotFound)
		return
	}
	ReturnESJSON(res, http.StatusOK, event)
}

// GetAttributes handles GET /v1/attributes/:attribute_name
func (p *v1Provider) GetAttributes(res http.ResponseWriter, req *http.Request) {
	token, ok := p.AuthHandler(res, req, "event:list")
	if !ok {
		return
	}

	// Handle QueryParams, Sanitize user input
	queryName := mux.Vars(req)["attribute_name"]
	queryName = strings.ReplaceAll(queryName, "\n", "")
	queryName = strings.ReplaceAll(queryName, "\r", "")
	if queryName == "" {
		logg.Debug("attribute_name empty")
		return
	}
	maxdepth, _ := strconv.ParseUint(req.FormValue("max_depth"), 10, 32) //nolint:errcheck
	limit, _ := strconv.ParseUint(req.FormValue("limit"), 10, 32)        //nolint:errcheck

	// Default Limit of 10000 if not specified by queryparam, which is the max opensearch supports.
	if limit == 0 {
		limit = 10000
	}

	// Reject limits above the configured storage maximum, matching the
	// offset+limit check applied to GET /v1/events in hermes.storageFilter.
	if maxLimit := p.storage.MaxLimit(); uint(limit) > maxLimit {
		http.Error(res, fmt.Sprintf("limit %d exceeds the maximum of %d", limit, maxLimit), http.StatusBadRequest)
		return
	}

	logg.Debug("api.GetAttributes: Create filter")
	filter := hermes.AttributeFilter{
		QueryName: queryName,
		MaxDepth:  uint(maxdepth),
		Limit:     uint(limit),
	}

	indexID, err := getIndexID(token, req, res)
	if err != nil {
		return
	}

	attribute, err := hermes.GetAttributes(req.Context(), &filter, indexID, p.storage)

	if errors.Is(err, storage.ErrUnknownAttributeName) {
		http.Error(res, err.Error(), http.StatusBadRequest)
		return
	}

	if respondwith.ErrorText(res, err) {
		logg.Error("could not get attributes from Storage: %s", err)
		storageErrorsCounter.Add(1)
		return
	}
	if attribute == nil {
		err := fmt.Errorf("attribute %s could not be found in project %s", queryName, indexID)
		http.Error(res, err.Error(), http.StatusNotFound)
		return
	}
	ReturnESJSON(res, http.StatusOK, attribute)
}

func getIndexID(token *gopherpolicy.Token, r *http.Request, w http.ResponseWriter) (string, error) {
	// Get index ID from a token
	// Defaults to a token project scope
	indexID := token.Context.Auth["project_id"]
	if indexID == "" {
		// Fallback to a token domain scope
		indexID = token.Context.Auth["domain_id"]
	}

	// Log and handle the case where neither project_id nor domain_id is found
	if indexID == "" {
		logg.Debug("Token context: %v", token.Context.Auth) // Log the token context for debugging
		logg.Error("Neither project_id nor domain_id found in token context")
	}

	// Sanitize user input
	projectid := r.FormValue("project_id")
	projectid = strings.ReplaceAll(projectid, "\n", "")
	projectid = strings.ReplaceAll(projectid, "\r", "")
	// When the projectid argument is defined, check for the cluster_viewer rule
	if v := projectid; v != "" {
		if !token.Require(w, "cluster_viewer") {
			// not a cloud admin, no possibility to override indexID
			return "", errors.New("cannot override index ID")
		}
		// Index ID can be overridden with a query parameter, when a cluster_viewer rule is used
		return v, nil
	}

	return indexID, nil
}
