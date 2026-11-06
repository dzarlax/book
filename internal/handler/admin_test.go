package handler

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type cancellationFixture struct {
	mu                  sync.Mutex
	state               string
	snapshot, eventID   string
	beginErr, finishErr error
}

func (s *cancellationFixture) BeginCancellation(context.Context, int64) (*storage.CancellationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "confirmed" || s.snapshot == "" || s.eventID == "" {
		return nil, storage.ErrBookingStateConflict
	}
	s.state = "cancelling"
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return &storage.CancellationTarget{CalendarID: s.snapshot, EventID: s.eventID}, nil
}
func (s *cancellationFixture) FinishCancellation(context.Context, int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishErr != nil {
		return s.finishErr
	}
	if s.state != "cancelling" {
		return storage.ErrBookingStateConflict
	}
	s.state = "cancelled"
	return nil
}

func TestCancellationUsesDurableOriginalTargetOnce(t *testing.T) {
	for _, scenario := range []string{"success", "delete_error", "lost_response", "finish_error", "begin_commit_error", "legacy_no_snapshot", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if r.Method != http.MethodDelete || r.URL.Query().Get("calendar_id") != "google:original" || r.URL.Query().Get("event_id") != "original-event" {
					t.Errorf("wrong cancellation target: %s %s", r.Method, r.URL)
				}
				if scenario == "delete_error" {
					http.Error(w, "synthetic error", 503)
					return
				}
				if scenario == "lost_response" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			store := &cancellationFixture{state: "confirmed", snapshot: "google:original", eventID: "original-event"}
			if scenario == "legacy_no_snapshot" {
				store.snapshot = ""
			}
			if scenario == "pending" {
				store.state = "pending"
			}
			if scenario == "begin_commit_error" {
				store.beginErr = errors.New("synthetic ambiguous commit")
			}
			if scenario == "finish_error" {
				store.finishErr = errors.New("synthetic ambiguous finish")
			}
			h := &AdminHandler{cancellations: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			post := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				h.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/bookings/1/cancel", nil))
				return w
			}
			result := post()
			wantCalls, wantStatus := 1, 503
			if scenario == "success" {
				wantStatus = 200
			}
			if scenario == "begin_commit_error" {
				wantCalls = 0
			}
			if scenario == "legacy_no_snapshot" || scenario == "pending" {
				wantCalls, wantStatus = 0, 409
			}
			mu.Lock()
			gotCalls := calls
			mu.Unlock()
			if result.Code != wantStatus || gotCalls != wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", result.Code, gotCalls, result.Body.String())
			}
			if scenario != "success" && store.state == "cancelled" {
				t.Fatal("unconfirmed delete released reservation")
			}
			if scenario == "success" && store.state != "cancelled" {
				t.Fatal("successful delete not finalized")
			}
			retry := post()
			mu.Lock()
			afterRetry := calls
			mu.Unlock()
			if retry.Code != 409 || afterRetry != wantCalls {
				t.Fatalf("repeat delete: status=%d calls=%d", retry.Code, afterRetry)
			}
		})
	}
}

func TestConcurrentCancellationDispatchesOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); calls++; mu.Unlock(); w.WriteHeader(204) }))
	defer server.Close()
	store := &cancellationFixture{state: "confirmed", snapshot: "google:original", eventID: "event"}
	h := &AdminHandler{cancellations: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/bookings/1/cancel", nil))
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	if calls != 1 || !((codes[0] == 200 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 200)) {
		t.Fatalf("calls=%d statuses=%v", calls, codes)
	}
}

func TestAdminBookingTemplateRequiresSnapshotForCancellation(t *testing.T) {
	funcs := template.FuncMap{"dayName": func(int) string { return "Day" }, "list": func(values ...string) []string { return values }, "lower": strings.ToLower}
	templates, err := template.New("").Funcs(funcs).ParseGlob("../ui/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"confirmed_snapshot", "confirmed_legacy", "pending", "unknown", "cancelling", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			b := model.Booking{ID: 1, Status: scenario, CalendarEvent: "event", StartTime: time.Now(), EndTime: time.Now().Add(time.Hour)}
			if strings.HasPrefix(scenario, "confirmed_") {
				b.Status = "confirmed"
			}
			if scenario == "confirmed_snapshot" {
				b.CalendarIDSnapshot = "google:original"
			}
			var out bytes.Buffer
			if err := templates.ExecuteTemplate(&out, "admin_bookings.html", map[string]any{"Title": "Bookings", "Bookings": []model.Booking{b}}); err != nil {
				t.Fatal(err)
			}
			offered := strings.Contains(out.String(), `hx-post="/admin/bookings/1/cancel"`)
			if offered != (scenario == "confirmed_snapshot") {
				t.Fatalf("cancel offered=%v for %s", offered, scenario)
			}
			if scenario == "confirmed_legacy" && !strings.Contains(out.String(), "Original calendar not recorded") {
				t.Fatal("legacy manual-review text missing")
			}
			if scenario == "cancelling" && !strings.Contains(out.String(), "Cancellation needs verification") {
				t.Fatal("cancelling review text missing")
			}
		})
	}
}
