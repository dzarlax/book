package calendarapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderCatalogValidatesCompletenessAndIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "empty", "unavailable", "incomplete", "missing_array", "null_array", "wrong_provider", "wrong_item_provider", "legacy_id", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			calendar := map[string]any{"id": "google:account:calendar", "provider": "google", "name": "Synthetic"}
			page := map[string]any{"provider": "google", "complete": true, "calendars": []any{calendar}}
			switch scenario {
			case "empty":
				page["calendars"] = []any{}
			case "incomplete":
				page["complete"] = false
			case "missing_array":
				delete(page, "calendars")
			case "null_array":
				page["calendars"] = nil
			case "wrong_provider":
				page["provider"] = "apple"
			case "wrong_item_provider":
				calendar["provider"] = "apple"
			case "legacy_id":
				calendar["id"] = "google:primary"
			case "duplicate":
				page["calendars"] = []any{calendar, calendar}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/availability/calendars" || r.URL.Query().Get("provider") != "google" {
					t.Errorf("unexpected request %s", r.URL)
				}
				if scenario == "unavailable" {
					http.Error(w, "unavailable", 503)
					return
				}
				json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			result, err := New(server.URL, "").ListProviderCalendars(t.Context(), "google")
			success := scenario == "valid" || scenario == "empty"
			if (err == nil) != success {
				t.Fatalf("result=%v err=%v", result, err)
			}
		})
	}
}

func TestBookingTargetRequiresExactPositiveCapabilities(t *testing.T) {
	for _, scenario := range []string{"valid", "read_only", "no_create", "missing_read_only", "missing_create", "wrong_id", "wrong_provider", "no_policy", "no_video", "ambiguous", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			id := "google:account:calendar"
			if scenario == "ambiguous" {
				id = "google:primary"
			}
			if scenario == "unsupported" {
				id = "microsoft:account:calendar"
			}
			ops := map[string]any{"create": true}
			page := map[string]any{"provider": "google", "calendar_id": "google:account:calendar", "read_only": false, "operations": ops, "fields": map[string]bool{"conferencing": true}, "notification_policies": []string{"none"}}
			switch scenario {
			case "read_only":
				page["read_only"] = true
			case "no_create":
				ops["create"] = false
			case "missing_read_only":
				delete(page, "read_only")
			case "missing_create":
				delete(ops, "create")
			case "wrong_id":
				page["calendar_id"] = "google:account:other"
			case "wrong_provider":
				page["provider"] = "apple"
			case "no_policy":
				page["notification_policies"] = []string{"all"}
			case "no_video":
				page["fields"] = map[string]bool{"conferencing": false}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v2/capabilities" || r.URL.Query().Get("calendar_id") != id {
					t.Error("wrong capability target")
				}
				json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			err := New(server.URL, "").CheckBookingTarget(t.Context(), id, true)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("err=%v", err)
			}
			if (scenario == "ambiguous" || scenario == "unsupported") && calls != 0 {
				t.Fatal("unsupported target queried provider")
			}
		})
	}
}
