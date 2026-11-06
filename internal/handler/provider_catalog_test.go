package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func providerCatalogFixture(w http.ResponseWriter, r *http.Request, failed string, create bool) bool {
	switch r.URL.Path {
	case "/api/v2/availability/calendars":
		provider := r.URL.Query().Get("provider")
		if provider == failed {
			http.Error(w, "unavailable", 503)
			return true
		}
		items := []calendarapi.Calendar{}
		if provider == "google" {
			items = []calendarapi.Calendar{{ID: "google:acct:old", Provider: provider, Name: "Old calendar"}, {ID: "google:acct:new", Provider: provider, Name: "New calendar"}}
		}
		if provider == "apple" {
			items = []calendarapi.Calendar{{ID: "apple:acct:old", Provider: provider, Name: "Saved Apple calendar"}}
		}
		json.NewEncoder(w).Encode(map[string]any{"provider": provider, "complete": true, "calendars": items})
		return true
	case "/api/v2/capabilities":
		json.NewEncoder(w).Encode(map[string]any{"provider": "google", "calendar_id": r.URL.Query().Get("calendar_id"), "read_only": !create, "operations": map[string]bool{"create": create}, "fields": map[string]bool{"conferencing": true}, "notification_policies": []string{"none"}})
		return true
	}
	return false
}

func TestAvailabilityQueriesOnlyRequiredProviders(t *testing.T) {
	for _, scenario := range []string{"google_only", "apple_selected", "all", "target_denied"} {
		t.Run(scenario, func(t *testing.T) {
			providers := []string{}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/availability/calendars" {
					providers = append(providers, r.URL.Query().Get("provider"))
				}
				if providerCatalogFixture(w, r, "apple", scenario != "target_denied") {
					return
				}
				if r.URL.Path == "/api/v2/availability/events" {
					writeAvailabilityPage(w, r, nil)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/events" {
					writes++
					fmt.Fprint(w, `{"id":"created"}`)
					return
				}
				t.Errorf("unexpected legacy/fallback request: %s %s", r.Method, r.URL)
				http.Error(w, "unexpected", 500)
			}))
			defer server.Close()
			store := &availabilityStore{setting: "google:acct:new", mt: model.MeetingType{ID: 1, Active: true, DurationMin: 30, CalendarID: "google:acct:new"}}
			if scenario == "apple_selected" {
				store.setting += "\napple:acct:old"
			}
			if scenario == "all" {
				store.setting = ""
			}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			day := time.Now().UTC().AddDate(0, 0, 3)
			start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
			form := url.Values{"start": {start.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"synthetic@example.invalid"}}
			req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			h.Routes().ServeHTTP(response, req)
			if scenario == "google_only" {
				if response.Code != 200 || writes != 1 || len(providers) != 1 || providers[0] != "google" {
					t.Fatalf("status=%d writes=%d providers=%v", response.Code, writes, providers)
				}
			} else if response.Code != 503 || writes != 0 || store.reserved != 0 || response.Header().Get("HX-Booking-State") != "not-created" {
				t.Fatalf("unsafe failure status=%d writes=%d reserved=%d providers=%v", response.Code, writes, store.reserved, providers)
			}
		})
	}
}

type settingsFixture struct {
	selection, keywords string
	err                 error
	saved               bool
}

func (s *settingsFixture) GetSetting(_ context.Context, key string) (string, error) {
	if key == "block_calendars" {
		return s.selection, nil
	}
	return s.keywords, nil
}
func (s *settingsFixture) UpdateBookingSettings(_ context.Context, expected, expectedKeywords, keywords, selection string) error {
	if s.err != nil {
		return s.err
	}
	if expected != s.selection || expectedKeywords != s.keywords {
		return storage.ErrSettingsConflict
	}
	s.selection, s.keywords, s.saved = selection, keywords, true
	return nil
}
func adminTemplates(t *testing.T) *template.Template {
	t.Helper()
	v, err := template.New("").Funcs(template.FuncMap{"dayName": func(int) string { return "Day" }, "list": func(v ...string) []string { return v }, "lower": strings.ToLower}).ParseGlob("../ui/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPartialCatalogAdminPreservesHiddenSelection(t *testing.T) {
	for _, scenario := range []string{"preserve", "explicit_remove", "recovered_after_render", "stale_form", "all_mode", "commit_error", "unknown_removal"} {
		t.Run(scenario, func(t *testing.T) {
			old := "google:acct:old\napple:acct:old"
			if scenario == "all_mode" {
				old = ""
			}
			store := &settingsFixture{selection: old, keywords: "old"}
			if scenario == "commit_error" {
				store.err = fmt.Errorf("synthetic commit")
			}
			failed := "apple"
			if scenario == "recovered_after_render" {
				failed = ""
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !providerCatalogFixture(w, r, failed, true) {
					t.Error("unexpected request")
				}
			}))
			defer server.Close()
			h := &AdminHandler{settingsStore: store, cal: calendarapi.New(server.URL, ""), tmpl: adminTemplates(t)}
			get := httptest.NewRecorder()
			h.Routes().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/settings", nil))
			if get.Code != 200 {
				t.Fatalf("settings not rendered: %d", get.Code)
			}
			if failed != "" && (!strings.Contains(get.Body.String(), "calendars are unavailable")) {
				t.Fatal("provider warning absent")
			}
			if old != "" && failed != "" && !strings.Contains(get.Body.String(), `name="remove_unavailable" value="apple:acct:old"`) {
				t.Fatal("explicit hidden-removal control absent")
			}
			form := url.Values{"selection_version": {selectionVersion(old, "old")}, "blocking_mode": {"selected"}, "block_calendars": {"google:acct:new"}, "visible_calendars": {"google:acct:old", "google:acct:new"}, "block_allday_keywords": {"new"}}
			if scenario == "explicit_remove" {
				form.Set("remove_unavailable", "apple:acct:old")
			}
			if scenario == "unknown_removal" {
				form.Set("remove_unavailable", "apple:acct:forged")
			}
			if scenario == "stale_form" {
				form.Set("selection_version", "stale")
			}
			if scenario == "all_mode" {
				form.Set("blocking_mode", "all")
			}
			req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			h.Routes().ServeHTTP(response, req)
			want := 200
			if scenario == "stale_form" {
				want = 409
			}
			if scenario == "commit_error" {
				want = 503
			}
			if scenario == "unknown_removal" {
				want = 400
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if want != 200 {
				if store.saved || store.selection != old || store.keywords != "old" {
					t.Fatal("failed save changed settings")
				}
				return
			}
			if scenario == "all_mode" {
				if store.selection != "" {
					t.Fatal("all mode narrowed to partial catalog")
				}
				return
			}
			if !strings.Contains(store.selection, "google:acct:new") {
				t.Fatal("explicit Google reconfiguration missing")
			}
			retained := strings.Contains(store.selection, "apple:acct:old")
			if retained != (scenario != "explicit_remove") {
				t.Fatalf("hidden selection retained=%v scenario=%s", retained, scenario)
			}
		})
	}
}

func TestSelectionMergePreservesProviderLostAtSave(t *testing.T) {
	old := "google:acct:old\napple:acct:old"
	known := map[string]calendarapi.Calendar{"apple:acct:old": {ID: "apple:acct:old", Provider: "apple"}}
	merged, err := mergeSelections(old, []string{"apple:acct:old"}, []string{"google:acct:old", "apple:acct:old"}, nil, "selected", known)
	if err != nil || !strings.Contains(merged, "google:acct:old") {
		t.Fatalf("lost provider erased selection: %q %v", merged, err)
	}
}

func TestStaleKeywordFormCannotOverwriteNewerSettings(t *testing.T) {
	store := &settingsFixture{selection: "google:acct:old", keywords: "old"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { providerCatalogFixture(w, r, "apple", true) }))
	defer server.Close()
	h := &AdminHandler{settingsStore: store, cal: calendarapi.New(server.URL, ""), tmpl: adminTemplates(t)}
	form := url.Values{"selection_version": {selectionVersion(store.selection, store.keywords)}, "blocking_mode": {"selected"}, "visible_calendars": {"google:acct:old"}, "block_calendars": {"google:acct:old"}, "block_allday_keywords": {"new"}}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		return w
	}
	if result := post(); result.Code != 200 {
		t.Fatalf("first save: %d %s", result.Code, result.Body.String())
	}
	form.Set("block_allday_keywords", "stale")
	if result := post(); result.Code != 409 || store.keywords != "new" {
		t.Fatalf("stale keywords saved: status=%d keywords=%q", result.Code, store.keywords)
	}
}

func TestChangedBlockingSettingsPreventReservationAndDispatch(t *testing.T) {
	for _, setting := range []string{"calendars", "keywords"} {
		t.Run(setting, func(t *testing.T) {
			store := &availabilityStore{setting: "google:acct:new", mt: model.MeetingType{ID: 1, Active: true, DurationMin: 30, CalendarID: "google:acct:new"}}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if providerCatalogFixture(w, r, "apple", true) {
					return
				}
				if r.URL.Path == "/api/v2/availability/events" {
					store.mu.Lock()
					if setting == "calendars" {
						store.setting = "google:acct:old"
					} else {
						store.blockKeywords = "new blocking keyword"
					}
					store.mu.Unlock()
					writeAvailabilityPage(w, r, nil)
					return
				}
				writes++
				t.Errorf("changed configuration reached writer: %s", r.URL)
			}))
			defer server.Close()
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			day := time.Now().UTC().AddDate(0, 0, 3)
			start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
			form := url.Values{"start": {start.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"synthetic@example.invalid"}}
			req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			if w.Code != 409 || w.Header().Get("HX-Booking-State") != "not-created" || store.reserved != 0 || writes != 0 {
				t.Fatalf("unsafe config race: status=%d reserved=%d writes=%d", w.Code, store.reserved, writes)
			}
		})
	}
}

type typeEditorFixture struct {
	mu      sync.Mutex
	mt      model.MeetingType
	updates int
}

func (s *typeEditorFixture) GetMeetingTypeByID(context.Context, int64) (*model.MeetingType, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := s.mt
	return &copy, nil
}
func (s *typeEditorFixture) UpdateMeetingTypeAtCalendar(_ context.Context, mt *model.MeetingType, expected string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mt.CalendarID != expected {
		return storage.ErrSettingsConflict
	}
	s.mt = *mt
	s.updates++
	return nil
}
func TestStaleTargetFormCannotRestorePreviousCalendar(t *testing.T) {
	for _, scenario := range []string{"already_changed", "changed_during_capability", "unavailable_retained"} {
		t.Run(scenario, func(t *testing.T) {
			editor := &typeEditorFixture{mt: model.MeetingType{ID: 1, CalendarID: "google:acct:old", DurationMin: 30}}
			if scenario == "already_changed" {
				editor.mt.CalendarID = "google:acct:current"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "changed_during_capability" {
					editor.mu.Lock()
					editor.mt.CalendarID = "google:acct:current"
					editor.mu.Unlock()
				}
				providerCatalogFixture(w, r, "apple", true)
			}))
			defer server.Close()
			h := &AdminHandler{meetingTypes: editor, cal: calendarapi.New(server.URL, "")}
			form := url.Values{"original_calendar_id": {"google:acct:old"}, "calendar_id": {"google:acct:new"}, "title": {"Synthetic"}, "slug": {"synthetic"}, "duration_min": {"30"}}
			if scenario == "already_changed" {
				form.Set("calendar_id", "google:acct:old")
			}
			if scenario == "unavailable_retained" {
				form.Del("calendar_id")
			}
			req := httptest.NewRequest(http.MethodPost, "/types/1", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			want := 409
			if scenario == "unavailable_retained" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if want == 409 && editor.updates != 0 {
				t.Fatal("stale target overwritten")
			}
			if scenario == "unavailable_retained" && editor.mt.CalendarID != "google:acct:old" {
				t.Fatal("hidden target cleared")
			}
		})
	}
}
