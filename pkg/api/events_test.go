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

// newFakeEventStore returns a store holding n events with predictable IDs.
func newFakeEventStore(n int, maxLimit uint) *fakeEventStore {
	s := &fakeEventStore{maxLimit: maxLimit}
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
	store := newFakeEventStore(3, 100)
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
	store := newFakeEventStore(3, 100)
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
			store := newFakeEventStore(tc.storedRows, 100)
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
