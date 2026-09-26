// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sapcc/go-api-declarations/cadf"

	"github.com/sapcc/hermes/pkg/storage"
)

// fakeEventStore is a storage.Storage that pages through a fixed list of
// events and records every EventFilter it receives.
type fakeEventStore struct {
	mu         sync.Mutex
	events     []*cadf.Event
	maxLimit   uint
	attributes []string
	filters    []storage.EventFilter
}

// newFakeEventStore returns a store holding n events with predictable IDs
// and a MaxLimit of 100, like storage.Mock.
func newFakeEventStore(n int) *fakeEventStore {
	s := &fakeEventStore{maxLimit: 100}
	for i := range n {
		s.events = append(s.events, &cadf.Event{
			ID:        fmt.Sprintf("00000000-0000-0000-0000-%012d", i),
			EventTime: "2026-01-01T00:00:00+00:00",
			Action:    cadf.CreateAction,
			Outcome:   cadf.SuccessOutcome,
		})
	}
	return s
}

func (s *fakeEventStore) GetEvents(_ context.Context, filter *storage.EventFilter, _ string) ([]*cadf.Event, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, *filter)
	start := min(int(filter.Offset), len(s.events))
	end := min(start+int(filter.Limit), len(s.events))
	return s.events[start:end], len(s.events), nil
}

func (s *fakeEventStore) GetEvent(context.Context, string, string) (*cadf.Event, error) {
	return nil, nil
}

func (s *fakeEventStore) GetAttributes(context.Context, *storage.AttributeFilter, string) ([]string, error) {
	return s.attributes, nil
}

func (s *fakeEventStore) MaxLimit() uint { return s.maxLimit }

// receivedFilters returns a copy of the filters seen so far.
func (s *fakeEventStore) receivedFilters() []storage.EventFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.EventFilter(nil), s.filters...)
}

func TestDownloadEvents_StreamsAllEvents(t *testing.T) {
	store := newFakeEventStore(3)
	router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)

	rec := doGet(t, router, "/v1/events/download?action=create")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "application/x-ndjson"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "audit-events.jsonl") {
		t.Errorf("Content-Disposition = %q, want an audit-events.jsonl attachment", got)
	}

	var ids []string
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		var ev struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("line %q is not a JSON event: %v", scanner.Text(), err)
		}
		ids = append(ids, ev.ID)
	}
	if len(ids) != 3 || ids[0] != store.events[0].ID || ids[2] != store.events[2].ID {
		t.Errorf("downloaded IDs = %v, want the 3 stored events in order", ids)
	}

	filters := store.receivedFilters()
	if len(filters) != 1 {
		t.Fatalf("storage called %d times, want 1", len(filters))
	}
	if f := filters[0]; f.Offset != 0 || f.Limit != 100 || f.Action != "create" {
		t.Errorf("storage filter = offset %d limit %d action %q, want offset 0 limit 100 action create", f.Offset, f.Limit, f.Action)
	}
}

func TestDownloadEvents_RejectsLongSearch(t *testing.T) {
	store := newFakeEventStore(3)
	router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)

	for _, tc := range []struct {
		name   string
		search string
		want   int
	}{
		{"AtLimit", strings.Repeat("a", storage.MaxSearchQueryLength), http.StatusOK},
		{"OverLimit", strings.Repeat("a", storage.MaxSearchQueryLength+1), http.StatusBadRequest},
		{"OverLimitMultibyte", strings.Repeat("ä", storage.MaxSearchQueryLength+1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(store.receivedFilters())
			rec := doGet(t, router, "/v1/events/download?search="+tc.search)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusBadRequest && len(store.receivedFilters()) != before {
				t.Error("an over-long search reached the storage layer")
			}
		})
	}
}

func TestListEvents_PaginationLinks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		wantPrev   string // expected offset in the previous link, "" for no link
		wantNext   string // expected offset in the next link, "" for no link
		storedRows int
	}{
		{"FirstPage", "offset=0&limit=10", "", "10", 50},
		{"OffsetBelowLimit", "offset=5&limit=10", "0", "15", 50},
		{"OffsetEqualsLimit", "offset=10&limit=10", "0", "20", 50},
		{"OffsetAboveLimit", "offset=25&limit=10", "15", "35", 50},
		{"LastPage", "offset=45&limit=10", "35", "", 50},
		{"DefaultLimit", "offset=3", "0", "13", 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeEventStore(tc.storedRows)
			router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)
			rec := doGet(t, router, "/v1/events?"+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			var list EventList
			if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			checkLinkOffset(t, "previous", list.PrevURL, tc.wantPrev)
			checkLinkOffset(t, "next", list.NextURL, tc.wantNext)
		})
	}
}

// checkLinkOffset asserts that link is empty when wantOffset is empty, and
// otherwise carries offset=wantOffset.
func checkLinkOffset(t *testing.T, name, link, wantOffset string) {
	t.Helper()
	if wantOffset == "" {
		if link != "" {
			t.Errorf("%s link = %q, want none", name, link)
		}
		return
	}
	u, err := url.Parse(link)
	if err != nil || link == "" {
		t.Fatalf("%s link = %q, want one with offset=%s", name, link, wantOffset)
	}
	if got := u.Query().Get("offset"); got != wantOffset {
		t.Errorf("%s link offset = %q, want %q (link %s)", name, got, wantOffset, link)
	}
}

func TestListEvents_SortTopicsReachStorage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sort      string
		wantCode  int
		wantField []string
	}{
		{"Current", "time:desc,action", http.StatusOK, []string{"time", "action"}},
		{"DeprecatedSource", "source:desc", http.StatusOK, []string{"observer_type"}},
		{"DeprecatedEventType", "event_type", http.StatusOK, []string{"action"}},
		{"DeprecatedResourceType", "resource_type:asc", http.StatusOK, []string{"resource_type"}},
		{"ResourceNameHasNoField", "resource_name", http.StatusBadRequest, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeEventStore(1)
			router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)
			rec := doGet(t, router, "/v1/events?sort="+tc.sort)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			filters := store.receivedFilters()
			if tc.wantCode != http.StatusOK {
				if len(filters) != 0 {
					t.Error("rejected sort reached the storage layer")
				}
				if !strings.Contains(rec.Body.String(), "valid topics: action,") {
					t.Errorf("400 body does not list the valid topics: %s", rec.Body.String())
				}
				return
			}
			if len(filters) != 1 {
				t.Fatalf("storage called %d times, want 1", len(filters))
			}
			var got []string
			for _, fo := range filters[0].Sort {
				got = append(got, fo.Fieldname)
			}
			if !slices.Equal(got, tc.wantField) {
				t.Errorf("sort fields = %v, want %v", got, tc.wantField)
			}
		})
	}
}

// TestSortTopicsHaveStorageFields guards against accepting a sort key that
// the storage layer cannot map to a field (it would build an empty sort key
// and OpenSearch would fail the whole query).
func TestSortTopicsHaveStorageFields(t *testing.T) {
	for topic := range validSortTopics {
		if storage.CADFFieldMapping[topic] == "" {
			t.Errorf("sort topic %q has no entry in storage.CADFFieldMapping", topic)
		}
	}
	for alias, topic := range deprecatedSortTopics {
		if !validSortTopics[topic] {
			t.Errorf("deprecated sort topic %q points to %q, which is not a valid topic", alias, topic)
		}
	}
}

func TestListEvents_LegacyFilterNames(t *testing.T) {
	type fields struct{ observerType, targetType, initiatorID, action string }
	for _, tc := range []struct {
		name  string
		query string
		want  fields
	}{
		{"CurrentOnly", "observer_type=service/compute&target_type=compute/server&initiator_id=u1&action=create",
			fields{"service/compute", "compute/server", "u1", "create"}},
		{"LegacyOnly", "source=service/compute&resource_type=compute/server&user_name=u1&event_type=create",
			fields{"service/compute", "compute/server", "u1", "create"}},
		{"BothSameValue", "observer_type=service/compute&source=service/compute&action=create&event_type=create",
			fields{"service/compute", "", "", "create"}},
		{"BothDifferentCurrentWins", "target_type=compute/server&resource_type=network/port&initiator_id=u1&user_name=u2",
			fields{"", "compute/server", "u1", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeEventStore(1)
			router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)
			rec := doGet(t, router, "/v1/events?"+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			filters := store.receivedFilters()
			if len(filters) != 1 {
				t.Fatalf("storage called %d times, want 1", len(filters))
			}
			f := filters[0]
			got := fields{f.ObserverType, f.TargetType, f.InitiatorID, f.Action}
			if got != tc.want {
				t.Errorf("filter = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGetAttributes_RejectsInvalidNumbers(t *testing.T) {
	router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, &fakeEventStore{maxLimit: 100, attributes: []string{"create"}})
	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"Valid", "/v1/attributes/action?max_depth=2&limit=5", http.StatusOK},
		{"EmptyValues", "/v1/attributes/action?max_depth=&limit=", http.StatusOK},
		{"MaxDepthWord", "/v1/attributes/action?max_depth=two", http.StatusBadRequest},
		{"MaxDepthNegative", "/v1/attributes/action?max_depth=-1", http.StatusBadRequest},
		{"LimitWord", "/v1/attributes/action?limit=abc", http.StatusBadRequest},
		{"LimitNegative", "/v1/attributes/action?limit=-1", http.StatusBadRequest},
		{"LimitTooLarge", "/v1/attributes/action?limit=99999999999", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := doGet(t, router, tc.path); rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestGetAttributes_NoValuesIsEmptyList(t *testing.T) {
	// fakeEventStore returns a nil slice when it has no attributes, like the
	// OpenSearch driver does for an aggregation without buckets.
	router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, &fakeEventStore{maxLimit: 100})
	rec := doGet(t, router, "/v1/attributes/action")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}
