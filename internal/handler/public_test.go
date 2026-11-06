package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type availabilityStore struct {
	publicStore
	onReserved       func()
	hoursErr         error
	globalHoursReads int
	mt               model.MeetingType
	setting          string
	blockKeywords    string
	settingErr       error
	hours            []model.WorkingHours
	bookings         []model.Booking
	created          int
	reserved         int
	reserveErr       error
	confirmErr       error
	pending          *model.Booking
	mu               sync.Mutex
}

func (s *availabilityStore) BookingSettings(context.Context) (model.BookingSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.BookingSettings{Calendars: s.setting, Keywords: s.blockKeywords}, s.settingErr
}
func (s *availabilityStore) GetMeetingType(context.Context, string) (*model.MeetingType, error) {
	return &s.mt, nil
}
func (s *availabilityStore) GetSetting(_ context.Context, key string) (string, error) {
	if key == "block_calendars" {
		return s.setting, s.settingErr
	}
	return "", nil
}
func (s *availabilityStore) ListMeetingTypeHours(context.Context, int64) ([]model.MeetingTypeHours, error) {
	return nil, s.hoursErr
}
func (s *availabilityStore) ListWorkingHours(context.Context) ([]model.WorkingHours, error) {
	s.globalHoursReads++
	if s.hours != nil {
		return s.hours, nil
	}
	hs := []model.WorkingHours{}
	for d := 0; d < 7; d++ {
		hs = append(hs, model.WorkingHours{DayOfWeek: d, StartTime: "09:00", EndTime: "17:00", Active: true})
	}
	return hs, nil
}
func (s *availabilityStore) GetBookingsForDay(context.Context, time.Time, int64) ([]model.Booking, error) {
	return s.bookings, nil
}
func (s *availabilityStore) CountBookingsForDay(context.Context, time.Time, int64) (int, error) {
	return 0, nil
}
func (s *availabilityStore) ReserveBooking(_ context.Context, b *model.Booking, _ *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserveErr != nil {
		return s.reserveErr
	}
	if b.ExpectedSettings == nil || *b.ExpectedSettings != (model.BookingSettings{Calendars: s.setting, Keywords: s.blockKeywords}) || b.ExpectedBufferMin != s.mt.BufferMin {
		return storage.ErrBookingStateConflict
	}
	if s.pending != nil {
		return storage.ErrBookingConflict
	}
	if b.CalendarIDSnapshot != s.mt.CalendarID {
		return storage.ErrBookingStateConflict
	}
	s.reserved++
	b.ID = 1
	b.Status = "pending"
	copy := *b
	s.pending = &copy
	if s.onReserved != nil {
		s.onReserved()
	}
	return nil
}
func (s *availabilityStore) ConfirmBooking(_ context.Context, id int64, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirmErr != nil {
		return s.confirmErr
	}
	if s.pending == nil || s.pending.ID != id {
		return storage.ErrBookingStateConflict
	}
	s.created++
	s.pending.Status, s.pending.CalendarEvent = "confirmed", eventID
	return nil
}
func (s *availabilityStore) GetBooking(context.Context, int64) (*model.Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil, nil
	}
	copy := *s.pending
	return &copy, nil
}

func TestAvailabilityRequiresExactAccessibleCalendars(t *testing.T) {
	for _, scenario := range []string{"catalog_error", "missing_selected", "event_error", "wrong_identity", "invalid_event", "explicit_success", "all_calendars"} {
		t.Run(scenario, func(t *testing.T) {
			queries := []string{}
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/calendars" {
					if scenario == "catalog_error" {
						http.Error(w, "offline", 503)
						return
					}
					fmt.Fprint(w, `[{"id":"google:acct:exact"},{"id":"google:acct:second"}]`)
					return
				}
				id := r.URL.Query().Get("calendar_id")
				queries = append(queries, id)
				if scenario == "event_error" {
					http.Error(w, "offline", 503)
					return
				}
				if scenario == "wrong_identity" {
					fmt.Fprint(w, `[{"calendar_id":"google:acct:other","start":"2030-01-01T09:00:00Z","end":"2030-01-01T10:00:00Z"}]`)
					return
				}
				if scenario == "invalid_event" {
					fmt.Fprint(w, `[{"calendar_id":"google:acct:exact"}]`)
					return
				}
				writeAvailabilityPage(w, r, nil)
			}))
			defer server.Close()
			store := &availabilityStore{setting: "google:acct:exact"}
			if scenario == "missing_selected" {
				store.setting = "google:primary"
			}
			if scenario == "all_calendars" {
				store.setting = ""
			}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			_, err := h.availabilityEvents(t.Context(), "", time.Now(), time.Now().Add(time.Hour))
			success := scenario == "explicit_success" || scenario == "all_calendars"
			if (err == nil) != success {
				t.Fatalf("err=%v queries=%v", err, queries)
			}
			if scenario == "missing_selected" && len(queries) != 0 {
				t.Fatalf("guessed an ID: %v", queries)
			}
			if success {
				want := 1
				if scenario == "all_calendars" {
					want = 2
				}
				if len(queries) != want {
					t.Fatalf("queries=%v", queries)
				}
				for _, id := range queries {
					if id == "" {
						t.Fatal("unsafe fan-out")
					}
				}
			}
		})
	}
}

func TestBookingRechecksAvailabilityBeforeMutation(t *testing.T) {
	day := time.Now().UTC().AddDate(0, 0, 2)
	start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"busy", "outage", "off_grid", "inactive", "old_target_id", "success"} {
		t.Run(scenario, func(t *testing.T) {
			writes := 0
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					writes++
					fmt.Fprint(w, `{"id":"google:created"}`)
					return
				}
				if r.URL.Path == "/api/calendars" {
					fmt.Fprint(w, `[{"id":"google:acct:exact"}]`)
					return
				}
				if scenario == "outage" {
					http.Error(w, "offline", 503)
					return
				}
				events := []calendarapi.Event{}
				if scenario == "busy" {
					events = append(events, calendarapi.Event{CalendarID: "google:acct:exact", Start: start, End: start.Add(time.Hour)})
				}
				writeAvailabilityPage(w, r, events)
			}))
			defer server.Close()
			store := &availabilityStore{setting: "google:acct:exact", mt: model.MeetingType{ID: 1, Slug: "meeting", DurationMin: 30, BufferMin: 0, CalendarID: "google:acct:exact", Active: scenario != "inactive"}}
			if scenario == "old_target_id" {
				store.mt.CalendarID = "google:primary"
			}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC, tmpl: template.Must(template.New("slots.html").Parse(`{{range .Slots}}{{if .Available}}available{{end}}{{end}}`))}
			selected := start
			if scenario == "off_grid" {
				selected = selected.Add(time.Minute)
			}
			form := url.Values{"start": {selected.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"test@example.invalid"}}
			req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			wantStatus, wantWrites, wantSaved := http.StatusConflict, 0, 0
			if scenario == "outage" || scenario == "inactive" || scenario == "old_target_id" {
				wantStatus = http.StatusServiceUnavailable
			}
			if scenario == "success" {
				wantStatus, wantWrites, wantSaved = http.StatusOK, 1, 1
			}
			if w.Code != wantStatus || writes != wantWrites || store.created != wantSaved {
				t.Fatalf("status=%d writes=%d saved=%d body=%s", w.Code, writes, store.created, w.Body.String())
			}
			if scenario == "outage" {
				get := httptest.NewRecorder()
				h.Routes().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/meeting/slots?date="+day.Format("2006-01-02"), nil))
				if get.Code != 503 || strings.Contains(get.Body.String(), "available</") {
					t.Fatalf("GET exposed availability: %d %s", get.Code, get.Body.String())
				}
			}
		})
	}
}

func TestBookingReservationGuardsProviderDispatch(t *testing.T) {
	day := time.Now().UTC().AddDate(0, 0, 3)
	start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"success", "reserve_commit_error", "reserve_conflict", "provider_timeout", "lost_response", "empty_event_id", "confirm_error", "no_calendar"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					mu.Lock()
					writes++
					mu.Unlock()
					if scenario == "provider_timeout" {
						select {
						case <-r.Context().Done():
						case <-time.After(250 * time.Millisecond):
						}
						return
					}
					if scenario == "lost_response" {
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					}
					if scenario == "empty_event_id" {
						fmt.Fprint(w, `{}`)
						return
					}
					fmt.Fprint(w, `{"id":"google:created"}`)
					return
				}
				if r.URL.Path == "/api/calendars" {
					fmt.Fprint(w, `[{"id":"google:acct:exact"}]`)
					return
				}
				writeAvailabilityPage(w, r, nil)
			}))
			defer server.Close()
			store := &availabilityStore{setting: "google:acct:exact", mt: model.MeetingType{ID: 1, Slug: "meeting", DurationMin: 30, CalendarID: "google:acct:exact", Active: true}}
			if scenario == "reserve_commit_error" {
				store.reserveErr = errors.New("synthetic ambiguous commit")
			}
			if scenario == "reserve_conflict" {
				store.reserveErr = storage.ErrBookingConflict
			}
			if scenario == "confirm_error" {
				store.confirmErr = errors.New("synthetic confirm failure")
			}
			if scenario == "no_calendar" {
				store.mt.CalendarID = ""
			}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC, tmpl: template.Must(template.New("confirmation.html").Parse(`Confirmed`))}
			post := func(timeout bool) *httptest.ResponseRecorder {
				form := url.Values{"start": {start.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"test@example.invalid"}}
				req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if timeout {
					ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
					defer cancel()
					req = req.WithContext(ctx)
				}
				w := httptest.NewRecorder()
				h.Routes().ServeHTTP(w, req)
				return w
			}
			response := post(scenario == "provider_timeout")
			wantStatus, wantWrites, wantState := 503, 1, "unknown"
			if scenario == "success" {
				wantStatus, wantState = 200, ""
			}
			if scenario == "reserve_commit_error" {
				wantWrites = 0
			}
			if scenario == "reserve_conflict" {
				wantStatus, wantWrites, wantState = 409, 0, "not-created"
			}
			if scenario == "no_calendar" {
				wantWrites, wantState = 0, "not-created"
			}
			mu.Lock()
			gotWrites := writes
			mu.Unlock()
			if response.Code != wantStatus || gotWrites != wantWrites || response.Header().Get("HX-Booking-State") != wantState {
				t.Fatalf("status=%d writes=%d state=%q body=%s", response.Code, gotWrites, response.Header().Get("HX-Booking-State"), response.Body.String())
			}
			if scenario == "success" {
				if store.created != 1 || store.pending.CalendarEvent == "" {
					t.Fatal("success not confirmed")
				}
			} else if store.created != 0 {
				t.Fatal("unconfirmed provider result saved confirmed")
			}
			if wantWrites == 1 && scenario != "success" {
				if store.pending == nil || store.pending.Status != "pending" {
					t.Fatalf("reservation not retained: %#v", store.pending)
				}
				retry := post(false)
				mu.Lock()
				afterRetry := writes
				mu.Unlock()
				if retry.Code != 409 || afterRetry != 1 {
					t.Fatalf("retry dispatched: status=%d writes=%d", retry.Code, afterRetry)
				}
				confirmation := httptest.NewRecorder()
				h.Routes().ServeHTTP(confirmation, httptest.NewRequest(http.MethodGet, "/confirmation/1", nil))
				if confirmation.Code != 409 || strings.Contains(confirmation.Body.String(), "You're booked!") {
					t.Fatalf("pending falsely confirmed: %d %s", confirmation.Code, confirmation.Body.String())
				}
			}
		})
	}
}

func TestConfirmationRequiresConfirmedEvent(t *testing.T) {
	for _, status := range []string{"pending", "unknown", "cancelled", "confirmed_without_event", "confirmed"} {
		t.Run(status, func(t *testing.T) {
			booking := &model.Booking{ID: 1, Status: status}
			if status == "confirmed_without_event" {
				booking.Status = "confirmed"
			}
			if status == "confirmed" {
				booking.CalendarEvent = "event"
			}
			h := &PublicHandler{store: &availabilityStore{pending: booking}, tmpl: template.Must(template.New("confirmation.html").Parse(`Confirmed`))}
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/confirmation/1", nil))
			want := 409
			if status == "confirmed" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAvailabilityIncludesBufferAcrossMidnight(t *testing.T) {
	day := time.Now().UTC().AddDate(0, 0, 3)
	midnight := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.UTC)
	for _, source := range []string{"calendar", "booking"} {
		t.Run(source, func(t *testing.T) {
			checked := false
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/calendars" {
					fmt.Fprint(w, `[{"id":"google:acct:exact"}]`)
					return
				}
				queryEnd, err := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
				if err != nil {
					t.Error(err)
				}
				checked = queryEnd.Equal(midnight.Add(30 * time.Minute))
				events := []calendarapi.Event{}
				if source == "calendar" && queryEnd.After(midnight.Add(10*time.Minute)) {
					events = append(events, calendarapi.Event{CalendarID: "google:acct:exact", Start: midnight.Add(10 * time.Minute), End: midnight.Add(20 * time.Minute)})
				}
				writeAvailabilityPage(w, r, events)
			}))
			defer server.Close()
			store := &availabilityStore{setting: "google:acct:exact", hours: []model.WorkingHours{{DayOfWeek: int(day.Weekday()), StartTime: "23:00", EndTime: "24:00", Active: true}}}
			if source == "booking" {
				store.bookings = []model.Booking{{StartTime: midnight.Add(10 * time.Minute), EndTime: midnight.Add(20 * time.Minute), Status: "pending"}}
			}
			mt := &model.MeetingType{ID: 1, CalendarID: "google:acct:exact", Active: true, DurationMin: 60, BufferMin: 30}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			slots, err := h.generateSlots(t.Context(), mt, day, "UTC")
			if err != nil || len(slots) != 1 || slots[0].Available || !checked {
				t.Fatalf("err=%v slots=%v extended=%v", err, slots, checked)
			}
		})
	}
}

func TestSlotsUnavailableWithoutConfiguredCalendar(t *testing.T) {
	for _, target := range []string{"", "google:acct:exact"} {
		t.Run(target, func(t *testing.T) {
			store := &availabilityStore{mt: model.MeetingType{ID: 1, Active: true, DurationMin: 30, CalendarID: target}}
			h := &PublicHandler{store: store, cal: calendarapi.New("", ""), timezone: time.UTC}
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/meeting/slots?date="+time.Now().AddDate(0, 0, 3).Format("2006-01-02"), nil))
			if w.Code != 503 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestV2AppleAvailabilitySupported(t *testing.T) {
	for _, selection := range []string{"apple:acct:/calendar/", ""} {
		t.Run(selection, func(t *testing.T) {
			reads := 0
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/calendars" {
					fmt.Fprint(w, `[{"id":"google:acct:exact","provider":"google"},{"id":"apple:acct:/calendar/","provider":"apple"}]`)
					return
				}
				reads++
				writeAvailabilityPage(w, r, nil)
			}))
			defer server.Close()
			h := &PublicHandler{store: &availabilityStore{setting: selection}, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			_, err := h.availabilityEvents(t.Context(), "google:acct:exact", time.Now(), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatalf("unsafe Apple availability: reads=%d err=%v", reads, err)
			}
		})
	}
}

func TestHoursReadFailureDoesNotUseGlobalSchedule(t *testing.T) {
	store := &availabilityStore{hoursErr: errors.New("synthetic type-hours failure")}
	h := &PublicHandler{store: store, cal: calendarapi.New("http://unused.invalid", ""), timezone: time.UTC}
	slots, err := h.generateSlots(t.Context(), &model.MeetingType{ID: 1, Active: true, CalendarID: "google:acct:exact", DurationMin: 30}, time.Now().AddDate(0, 0, 3), "UTC")
	if err == nil || len(slots) > 0 || store.globalHoursReads != 0 {
		t.Fatalf("unsafe fallback: err=%v slots=%v global reads=%d", err, slots, store.globalHoursReads)
	}
}

func TestV2MicrosoftAvailabilitySupported(t *testing.T) {
	for _, selection := range []string{"microsoft:acct:calendar", ""} {
		t.Run(selection, func(t *testing.T) {
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/calendars" {
					fmt.Fprint(w, `[{"id":"google:acct:exact","provider":"google"},{"id":"microsoft:acct:calendar","provider":"microsoft"}]`)
					return
				}
				writeAvailabilityPage(w, r, nil)
			}))
			defer server.Close()
			h := &PublicHandler{store: &availabilityStore{setting: selection}, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			_, err := h.availabilityEvents(t.Context(), "google:acct:exact", time.Now(), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatalf("unsafe Microsoft availability: %v", err)
			}
		})
	}
}

func writeAvailabilityPage(w http.ResponseWriter, r *http.Request, events []calendarapi.Event) {
	id := r.URL.Query().Get("calendar_id")
	provider := strings.SplitN(strings.SplitN(id, ":", 2)[0], "@", 2)[0]
	items := []map[string]any{}
	for i, event := range events {
		eventID := event.ID
		if eventID == "" {
			eventID = fmt.Sprintf("synthetic-%d", i)
		}
		start, end := map[string]string{"date_time": event.Start.Format(time.RFC3339), "time_zone": "UTC"}, map[string]string{"date_time": event.End.Format(time.RFC3339), "time_zone": "UTC"}
		if event.AllDay {
			start, end = map[string]string{"date": event.Start.Format("2006-01-02")}, map[string]string{"date": event.End.Format("2006-01-02")}
		}
		items = append(items, map[string]any{"id": eventID, "calendar_id": event.CalendarID, "provider": provider, "title": event.Title, "start": start, "end": end})
	}
	json.NewEncoder(w).Encode(map[string]any{"items": items, "complete": true, "sources": []map[string]any{{"calendar_id": id, "provider": provider, "complete": true}}})
}

func TestBookingCreateUsesReservedCalendarSnapshot(t *testing.T) {
	day := time.Now().UTC().AddDate(0, 0, 3)
	start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
	target := ""
	server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var body calendarapi.CreateEventRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			target = body.CalendarID
			fmt.Fprint(w, `{"id":"event"}`)
			return
		}
		if r.URL.Path == "/api/calendars" {
			fmt.Fprint(w, `[{"id":"google:acct:exact"}]`)
			return
		}
		writeAvailabilityPage(w, r, nil)
	}))
	defer server.Close()
	store := &availabilityStore{setting: "google:acct:exact", mt: model.MeetingType{ID: 1, Active: true, DurationMin: 30, CalendarID: "google:acct:exact"}}
	store.onReserved = func() { store.mt.CalendarID = "google:changed-after-reservation" }
	h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
	form := url.Values{"start": {start.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"test@example.invalid"}}
	req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	h.Routes().ServeHTTP(response, req)
	if response.Code != 200 || target != "google:acct:exact" || store.pending.CalendarIDSnapshot != "google:acct:exact" {
		t.Fatalf("status=%d target=%q snapshot=%q", response.Code, target, store.pending.CalendarIDSnapshot)
	}
}

func TestMicrosoftBookingTargetRejectedBeforeReservation(t *testing.T) {
	day := time.Now().UTC().AddDate(0, 0, 3)
	start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"microsoft@account:calendar", "microsoft:account:calendar"} {
		t.Run(id, func(t *testing.T) {
			calls := 0
			server := newCalendarFixtureServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				t.Errorf("unsupported target reached calendar API: %s %s", r.Method, r.URL)
				http.Error(w, "unexpected", 500)
			}))
			defer server.Close()
			store := &availabilityStore{mt: model.MeetingType{ID: 1, Active: true, DurationMin: 30, CalendarID: id}}
			h := &PublicHandler{store: store, cal: calendarapi.New(server.URL, ""), timezone: time.UTC}
			form := url.Values{"start": {start.Format("2006-01-02T15:04")}, "tz": {"UTC"}, "name": {"Synthetic"}, "email": {"test@example.invalid"}}
			req := httptest.NewRequest(http.MethodPost, "/meeting/book", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			h.Routes().ServeHTTP(response, req)
			if response.Code != 503 || response.Header().Get("HX-Booking-State") != "not-created" || store.reserved != 0 || store.pending != nil || calls != 0 {
				t.Fatalf("status=%d state=%q reserved=%d pending=%v calls=%d", response.Code, response.Header().Get("HX-Booking-State"), store.reserved, store.pending, calls)
			}
			get := httptest.NewRecorder()
			h.Routes().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/meeting/slots?date="+day.Format("2006-01-02"), nil))
			if get.Code != 503 || calls != 0 {
				t.Fatalf("unsupported GET: status=%d calls=%d", get.Code, calls)
			}
		})
	}
}
