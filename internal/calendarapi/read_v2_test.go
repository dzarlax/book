package calendarapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func validV2Page() map[string]any {
	return map[string]any{"items": []any{}, "complete": true, "sources": []any{map[string]any{"calendar_id": "google:exact", "provider": "google", "complete": true}}}
}
func validV2Event() map[string]any {
	return map[string]any{"id": "event", "calendar_id": "google:exact", "provider": "google", "title": "Synthetic", "start": map[string]any{"date_time": "2030-11-05T09:00:00Z", "time_zone": "UTC"}, "end": map[string]any{"date_time": "2030-11-05T10:00:00Z", "time_zone": "UTC"}}
}
func TestV2AvailabilityRejectsUnprovedPages(t *testing.T) {
	for _, scenario := range []string{"missing_items", "incomplete", "missing_complete", "missing_sources", "source_incomplete", "source_error", "source_identity", "provider_identity", "event_identity", "event_provider", "missing_event_id", "invalid_time", "offset_only", "mixed_time", "invalid_zone", "recurring_master", "trailing_json", "legacy_array", "explicit_empty_null", "timed", "all_day"} {
		t.Run(scenario, func(t *testing.T) {
			page := validV2Page()
			event := validV2Event()
			page["items"] = []any{event}
			source := page["sources"].([]any)[0].(map[string]any)
			switch scenario {
			case "missing_items":
				delete(page, "items")
			case "incomplete":
				page["complete"] = false
			case "missing_complete":
				delete(page, "complete")
			case "missing_sources":
				delete(page, "sources")
			case "source_incomplete":
				source["complete"] = false
			case "source_error":
				source["error"] = map[string]string{"code": "unavailable"}
			case "source_identity":
				source["calendar_id"] = "google:other"
			case "provider_identity":
				source["provider"] = "microsoft"
				event["provider"] = "microsoft"
			case "event_identity":
				event["calendar_id"] = "google:other"
			case "event_provider":
				event["provider"] = "microsoft"
			case "missing_event_id":
				delete(event, "id")
			case "invalid_time":
				event["start"] = map[string]string{"date_time": "bad", "time_zone": "UTC"}
			case "offset_only":
				event["start"] = map[string]string{"date_time": "2030-11-05T11:00:00+02:00"}
			case "mixed_time":
				event["start"] = map[string]string{"date": "2030-11-05"}
			case "invalid_zone":
				event["start"] = map[string]string{"date_time": "2030-11-05T09:00:00Z", "time_zone": "Local"}
			case "recurring_master":
				event["recurrence"] = []string{"RRULE:FREQ=DAILY"}
			case "explicit_empty_null":
				page["items"] = nil
			case "all_day":
				event["start"] = map[string]string{"date": "2030-11-05"}
				event["end"] = map[string]string{"date": "2030-11-06"}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/availability/events" || r.URL.Query().Get("view") != "expanded" || r.URL.Query().Get("calendar_id") != "google:exact" {
					t.Errorf("incorrect read request: %s", r.URL)
				}
				if scenario == "legacy_array" {
					fmt.Fprint(w, `[]`)
					return
				}
				json.NewEncoder(w).Encode(page)
				if scenario == "trailing_json" {
					fmt.Fprint(w, `{}`)
				}
			}))
			defer server.Close()
			client := New(server.URL, "")
			events, err := client.GetEvents(context.Background(), "google:exact", time.Date(2030, 11, 5, 0, 0, 0, 0, time.UTC), time.Date(2030, 11, 6, 0, 0, 0, 0, time.UTC))
			success := scenario == "timed" || scenario == "all_day" || scenario == "explicit_empty_null" || scenario == "offset_only"
			if (err == nil) != success {
				t.Fatalf("events=%v err=%v", events, err)
			}
			if !success && events != nil {
				t.Fatal("partial events escaped")
			}
			if scenario == "offset_only" && (len(events) != 1 || events[0].Start.UTC().Hour() != 9) {
				t.Fatalf("RFC3339 offset was not preserved: %v", events)
			}
			if scenario == "all_day" && (len(events) != 1 || !events[0].AllDay) {
				t.Fatalf("all day conversion: %v", events)
			}
		})
	}
}

func TestV2AvailabilityPaginationIsBoundedAndAtomic(t *testing.T) {
	for _, scenario := range []string{"two_pages", "cursor_loop", "later_error", "endless", "later_incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				page := validV2Page()
				event := validV2Event()
				event["id"] = fmt.Sprint(calls)
				page["items"] = []any{event}
				if calls == 1 {
					page["next_page_token"] = "opaque + / cursor"
				} else if scenario == "cursor_loop" {
					page["next_page_token"] = "opaque + / cursor"
				} else if scenario == "endless" {
					page["next_page_token"] = fmt.Sprint(calls)
				} else if scenario == "later_error" {
					http.Error(w, "unavailable", 503)
					return
				} else if scenario == "later_incomplete" {
					page["complete"] = false
				}
				if calls == 2 && r.URL.Query().Get("page_token") != "opaque + / cursor" {
					t.Errorf("cursor not preserved: %s", r.URL)
				}
				json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			events, err := New(server.URL, "").GetEvents(t.Context(), "google:exact", time.Now(), time.Now().Add(time.Hour))
			if scenario == "two_pages" {
				if err != nil || len(events) != 2 {
					t.Fatalf("events=%v err=%v", events, err)
				}
			} else if err == nil || events != nil {
				t.Fatalf("partial prefix returned: %v %v", events, err)
			}
			if calls > 100 {
				t.Fatalf("unbounded pagination: %d", calls)
			}
			if scenario == "endless" && !strings.Contains(err.Error(), "limit") {
				t.Fatalf("missing limit error: %v", err)
			}
		})
	}
}

func TestV2AvailabilityValidatesCalendarInstanceKinds(t *testing.T) {
	// Calendar currently uses string fields rather than a closed Go enum:
	// Google/Apple: occurrence, exception, cancelled, seriesMaster;
	// Microsoft normalizes singleInstance to empty; orphanOccurrence is the
	// explicit shared Calendar constant for a readable orphan Google instance.
	for _, kind := range []string{"", "occurrence", "exception", "cancelled", "orphanOccurrence", "seriesMaster", "singleInstance", "cache_instance", "future_kind"} {
		t.Run("kind_"+kind, func(t *testing.T) {
			page := validV2Page()
			event := validV2Event()
			event["instance_kind"] = kind
			page["items"] = []any{event}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(page) }))
			defer server.Close()
			events, err := New(server.URL, "").GetEvents(t.Context(), "google:exact", time.Now(), time.Now().Add(time.Hour))
			allowed := kind == "" || kind == "occurrence" || kind == "exception" || kind == "cancelled" || kind == "orphanOccurrence"
			if (err == nil) != allowed {
				t.Fatalf("kind=%q events=%v err=%v", kind, events, err)
			}
			if !allowed && events != nil {
				t.Fatal("unrecognized instance returned")
			}
		})
	}
}

func TestAvailabilityEndpointHasNoFallback(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls = append(calls, r.URL.Path); http.NotFound(w, r) }))
	defer server.Close()
	events, err := New(server.URL, "").GetEvents(t.Context(), "google:exact", time.Now(), time.Now().Add(time.Hour))
	if err == nil || events != nil || len(calls) != 1 || calls[0] != "/api/v2/availability/events" {
		t.Fatalf("unsafe fallback: calls=%v events=%v err=%v", calls, events, err)
	}
}
