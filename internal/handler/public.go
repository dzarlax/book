package handler

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type publicStore interface {
	ListMeetingTypes(context.Context, bool) ([]model.MeetingType, error)
	GetMeetingType(context.Context, string) (*model.MeetingType, error)
	ListMeetingTypeHours(context.Context, int64) ([]model.MeetingTypeHours, error)
	ListWorkingHours(context.Context) ([]model.WorkingHours, error)
	GetBookingsForDay(context.Context, time.Time, int64) ([]model.Booking, error)
	CountBookingsForDay(context.Context, time.Time, int64) (int, error)
	GetSetting(context.Context, string) (string, error)
	ReserveBooking(context.Context, *model.Booking, *time.Location) error
	ConfirmBooking(context.Context, int64, string) error
	GetBooking(context.Context, int64) (*model.Booking, error)
}

type PublicHandler struct {
	store    publicStore
	cal      *calendarapi.Client
	tmpl     *template.Template
	timezone *time.Location
}

func NewPublicHandler(store *storage.Storage, cal *calendarapi.Client, tmpl *template.Template, tz *time.Location) *PublicHandler {
	return &PublicHandler{store: store, cal: cal, tmpl: tmpl, timezone: tz}
}

func (h *PublicHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.index)
	r.Get("/{slug}", h.meetingPage)
	r.Get("/{slug}/slots", h.slots)
	r.Post("/{slug}/book", h.book)
	r.Get("/confirmation/{id}", h.confirmation)
	return r
}

func (h *PublicHandler) index(w http.ResponseWriter, r *http.Request) {
	types, err := h.store.ListMeetingTypes(r.Context(), true)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	h.render(w, "index.html", map[string]any{"Title": "Book a Meeting", "Types": types})
}

func (h *PublicHandler) meetingPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	mt, err := h.store.GetMeetingType(r.Context(), slug)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if mt == nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, "meeting.html", map[string]any{
		"Title":   mt.Title + " — Book",
		"Meeting": mt,
		"Today":   time.Now().In(h.timezone).Format("2006-01-02"),
	})
}

func (h *PublicHandler) slots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	slug := chi.URLParam(r, "slug")
	dateStr := r.URL.Query().Get("date")
	guestTZ := r.URL.Query().Get("tz")
	if guestTZ == "" {
		guestTZ = h.timezone.String()
	}

	mt, err := h.store.GetMeetingType(r.Context(), slug)
	if err != nil || mt == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "Invalid date", http.StatusBadRequest)
		return
	}

	slots, err := h.generateSlots(r.Context(), mt, date, guestTZ)
	if err != nil {
		http.Error(w, "Availability temporarily unavailable", http.StatusServiceUnavailable)
		return
	}

	h.render(w, "slots.html", map[string]any{
		"Slots":   slots,
		"Meeting": mt,
		"Date":    dateStr,
		"GuestTZ": guestTZ,
	})
}

func (h *PublicHandler) generateSlots(ctx context.Context, mt *model.MeetingType, date time.Time, guestTZ string) ([]model.TimeSlot, error) {
	if mt.CalendarID == "" || !h.cal.Enabled() {
		return nil, fmt.Errorf("booking calendar unavailable")
	}
	route, _, _ := strings.Cut(mt.CalendarID, ":")
	provider, _, _ := strings.Cut(route, "@")
	// Legacy Microsoft creation rejects guest attendees; do not reserve a slot
	// for a write contract that cannot create this booking.
	if provider == "microsoft" {
		return nil, fmt.Errorf("Microsoft booking creation is unavailable with the current calendar write API")
	}
	if !mt.Active || mt.DurationMin <= 0 || mt.BufferMin < 0 {
		return nil, fmt.Errorf("meeting type unavailable")
	}
	dayDate := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, h.timezone)
	weekday := int(dayDate.Weekday())

	// Prefer per-type hours, fall back to global working hours
	startTime, endTime, found, err := h.resolveHours(ctx, mt.ID, weekday)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil // not a working day
	}

	startH, startM := parseTime(startTime)
	endH, endM := parseTime(endTime)
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), startH, startM, 0, 0, h.timezone)
	dayEnd := time.Date(date.Year(), date.Month(), date.Day(), endH, endM, 0, 0, h.timezone)

	// Get bookings from our DB
	bookings, err := h.store.GetBookingsForDay(ctx, dayDate, mt.ID)
	if err != nil {
		return nil, err
	}

	count, err := h.store.CountBookingsForDay(ctx, dayDate, mt.ID)
	if err != nil {
		return nil, err
	}

	if mt.MaxPerDay > 0 && count >= mt.MaxPerDay {
		return nil, nil // daily limit reached
	}

	calEvents, err := h.availabilityEvents(ctx, mt.CalendarID, dayStart, dayEnd.Add(time.Duration(mt.BufferMin)*time.Minute))
	if err != nil {
		return nil, err
	}
	rawKeywords, err := h.store.GetSetting(ctx, "block_allday_keywords")
	if err != nil {
		return nil, err
	}
	blockKeywords := []string{}
	for _, keyword := range strings.Split(strings.ToLower(rawKeywords), "\n") {
		if keyword = strings.TrimSpace(keyword); keyword != "" {
			blockKeywords = append(blockKeywords, keyword)
		}
	}
	for _, ev := range calEvents {
		if ev.AllDay && matchesAnyKeyword(ev.Title, blockKeywords) {
			return nil, nil // whole day blocked
		}
	}

	guestLoc, err := time.LoadLocation(guestTZ)
	if err != nil {
		guestLoc = h.timezone
	}

	duration := time.Duration(mt.DurationMin) * time.Minute
	step := duration + time.Duration(mt.BufferMin)*time.Minute
	now := time.Now().In(h.timezone)

	var slots []model.TimeSlot
	for t := dayStart; t.Add(duration).Before(dayEnd) || t.Add(duration).Equal(dayEnd); t = t.Add(step) {
		if t.Before(now) {
			continue
		}

		available := true
		slotEnd := t.Add(duration)
		bufferedEnd := slotEnd.Add(time.Duration(mt.BufferMin) * time.Minute)

		// Check against bookings in our DB
		for _, b := range bookings {
			if t.Before(b.EndTime) && bufferedEnd.After(b.StartTime) {
				available = false
				break
			}
		}

		// Check against real calendar events (free/busy), skip all-day events
		if available {
			for _, ev := range calEvents {
				if ev.AllDay {
					continue
				}
				if t.Before(ev.End) && bufferedEnd.After(ev.Start) {
					available = false
					break
				}
			}
		}

		slots = append(slots, model.TimeSlot{
			Start:     t.In(guestLoc),
			End:       slotEnd.In(guestLoc),
			Available: available,
		})
	}
	return slots, nil
}

func (h *PublicHandler) book(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("HX-Booking-State", "not-created")
	slug := chi.URLParam(r, "slug")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	mt, err := h.store.GetMeetingType(r.Context(), slug)
	if err != nil || mt == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	startStr := r.FormValue("start")
	guestTZ := r.FormValue("tz")
	guestName := r.FormValue("name")
	guestEmail := r.FormValue("email")
	subject := r.FormValue("subject")

	if guestName == "" || guestEmail == "" || startStr == "" {
		http.Error(w, "Missing fields", http.StatusBadRequest)
		return
	}

	guestLoc, err := time.LoadLocation(guestTZ)
	if err != nil {
		guestLoc = h.timezone
	}

	startTime, err := time.ParseInLocation("2006-01-02T15:04", startStr, guestLoc)
	if err != nil {
		http.Error(w, "Invalid time", http.StatusBadRequest)
		return
	}

	slots, err := h.generateSlots(r.Context(), mt, startTime.In(h.timezone), guestLoc.String())
	if err != nil {
		http.Error(w, "Availability temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	available := false
	for _, slot := range slots {
		if slot.Available && slot.Start.Equal(startTime) {
			available = true
			break
		}
	}
	if !available {
		http.Error(w, "This slot is no longer available", http.StatusConflict)
		return
	}
	if mt.CalendarID == "" || !h.cal.Enabled() {
		http.Error(w, "Calendar temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	endTime := startTime.Add(time.Duration(mt.DurationMin) * time.Minute)

	booking := &model.Booking{
		MeetingTypeID: mt.ID, GuestName: guestName, GuestEmail: guestEmail,
		StartTime: startTime.UTC(), EndTime: endTime.UTC(), Timezone: guestLoc.String(),
		Status: "pending", CalendarIDSnapshot: mt.CalendarID,
	}
	// A failed commit may already have reserved the slot. Never dispatch after
	// any reservation error, and never tell the guest it is safe to retry one.
	w.Header().Set("HX-Booking-State", "unknown")
	if err := h.store.ReserveBooking(r.Context(), booking, h.timezone); err != nil {
		if errors.Is(err, storage.ErrBookingConflict) || errors.Is(err, storage.ErrBookingLimit) || errors.Is(err, storage.ErrBookingStateConflict) {
			w.Header().Set("HX-Booking-State", "not-created")
			http.Error(w, "This slot is no longer available", http.StatusConflict)
		} else {
			http.Error(w, "Booking reservation could not be confirmed. Contact the host before retrying.", http.StatusServiceUnavailable)
		}
		return
	}
	if booking.ID <= 0 {
		http.Error(w, "Booking reservation could not be confirmed. Contact the host before retrying.", http.StatusServiceUnavailable)
		return
	}
	eventTitle := fmt.Sprintf("%s with %s", mt.Title, guestName)
	eventDesc := fmt.Sprintf("Booked via book.dzarlax.dev\nGuest: %s (%s)", guestName, guestEmail)
	if subject != "" {
		eventTitle = fmt.Sprintf("%s: %s", guestName, subject)
		eventDesc = fmt.Sprintf("Type: %s\nGuest: %s (%s)\nSubject: %s\n\nBooked via book.dzarlax.dev", mt.Title, guestName, guestEmail, subject)
	}
	// One provider attempt only, after the durable reservation committed.
	ev, err := h.cal.CreateEvent(r.Context(), calendarapi.CreateEventRequest{
		CalendarID: booking.CalendarIDSnapshot, Title: eventTitle,
		Start: startTime.UTC().Format(time.RFC3339), End: endTime.UTC().Format(time.RFC3339),
		Description: eventDesc, Attendees: []calendarapi.Attendee{{Email: guestEmail, Name: guestName}}, VideoCall: mt.VideoCall,
	})
	if err != nil || ev == nil || strings.TrimSpace(ev.ID) == "" {
		log.Printf("calendar booking outcome unconfirmed for reservation %d", booking.ID)
		http.Error(w, "The booking may already exist. Contact the host before booking again.", http.StatusServiceUnavailable)
		return
	}
	if err := h.store.ConfirmBooking(r.Context(), booking.ID, ev.ID); err != nil {
		http.Error(w, "The booking may already exist. Contact the host before booking again.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Del("HX-Booking-State")
	w.Header().Set("HX-Redirect", fmt.Sprintf("/confirmation/%d", booking.ID))
	w.WriteHeader(http.StatusOK)
}

func (h *PublicHandler) confirmation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	booking, err := h.store.GetBooking(r.Context(), id)
	if err != nil {
		w.Header().Set("HX-Booking-State", "unknown")
		http.Error(w, "Booking status is temporarily unavailable. Contact the host before booking again.", http.StatusServiceUnavailable)
		return
	}
	if booking == nil {
		http.NotFound(w, r)
		return
	}
	if booking.Status != "confirmed" || strings.TrimSpace(booking.CalendarEvent) == "" {
		w.Header().Set("HX-Booking-State", "unknown")
		http.Error(w, "This booking is not confirmed. Contact the host to verify the calendar before booking again.", http.StatusConflict)
		return
	}
	h.render(w, "confirmation.html", map[string]any{"Title": "Booking Confirmed — Book", "ID": id})
}

func (h *PublicHandler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func parseTime(s string) (int, int) {
	var h, m int
	fmt.Sscanf(s, "%d:%d", &h, &m)
	return h, m
}

func (h *PublicHandler) resolveHours(ctx context.Context, meetingTypeID int64, weekday int) (start, end string, found bool, err error) {
	mtHours, err := h.store.ListMeetingTypeHours(ctx, meetingTypeID)
	if err != nil {
		return "", "", false, err
	}
	for _, h := range mtHours {
		if h.DayOfWeek == weekday && h.Active {
			return h.StartTime, h.EndTime, true, nil
		}
	}
	if len(mtHours) > 0 {
		return "", "", false, nil
	}
	wh, err := h.store.ListWorkingHours(ctx)
	if err != nil {
		return "", "", false, err
	}
	for _, h := range wh {
		if h.DayOfWeek == weekday && h.Active {
			return h.StartTime, h.EndTime, true, nil
		}
	}
	return "", "", false, nil
}

func (h *PublicHandler) getBlockKeywords(ctx context.Context) []string {
	raw, err := h.store.GetSetting(ctx, "block_allday_keywords")
	if err != nil || raw == "" {
		return nil
	}
	var keywords []string
	for _, k := range strings.Split(raw, "\n") {
		k = strings.TrimSpace(k)
		if k != "" {
			keywords = append(keywords, strings.ToLower(k))
		}
	}
	return keywords
}

func matchesAnyKeyword(title string, keywords []string) bool {
	lower := strings.ToLower(title)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (h *PublicHandler) getBlockCalendars(ctx context.Context) []string {
	raw, err := h.store.GetSetting(ctx, "block_calendars")
	if err != nil || raw == "" {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(raw, "\n") {
		id = strings.TrimSpace(id)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func filterByCalendars(events []calendarapi.Event, calIDs []string) []calendarapi.Event {
	allowed := make(map[string]bool, len(calIDs))
	for _, id := range calIDs {
		allowed[id] = true
	}
	filtered := events[:0]
	for _, ev := range events {
		if allowed[ev.CalendarID] {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

// availabilityEvents requires each exact configured calendar to be readable.
// Each exact ID must pass the complete expanded V2 read contract.
func (h *PublicHandler) availabilityEvents(ctx context.Context, targetID string, start, end time.Time) ([]calendarapi.Event, error) {
	raw, err := h.store.GetSetting(ctx, "block_calendars")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, id := range strings.Split(raw, "\n") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if !h.cal.Enabled() {
		if len(ids) > 0 || targetID != "" {
			return nil, fmt.Errorf("configured calendars unavailable")
		}
		return nil, nil
	}
	calendars, err := h.cal.ListCalendars(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	writableTarget := false
	for _, cal := range calendars {
		if cal.ID == "" {
			return nil, fmt.Errorf("invalid calendar catalog")
		}
		known[cal.ID] = true

		if cal.ID == targetID && !cal.ReadOnly {
			writableTarget = true
		}
	}
	if targetID != "" && !writableTarget {
		return nil, fmt.Errorf("booking calendar unavailable")
	}
	if len(known) == 0 {
		return nil, fmt.Errorf("calendar catalog unavailable")
	}
	if len(ids) == 0 {
		for _, cal := range calendars {
			ids = append(ids, cal.ID)
		}
	}
	events := []calendarapi.Event{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("configured calendar unavailable")
		}

		if seen[id] {
			continue
		}
		seen[id] = true
		page, err := h.cal.GetEvents(ctx, id, start, end)
		if err != nil {
			return nil, err
		}
		for _, event := range page {
			if event.CalendarID != id || event.Start.IsZero() || !event.End.After(event.Start) {
				return nil, fmt.Errorf("calendar events incomplete or invalid")
			}
		}
		events = append(events, page...)
	}
	return events, nil
}
