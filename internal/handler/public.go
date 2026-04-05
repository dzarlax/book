package handler

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type PublicHandler struct {
	store    *storage.Storage
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
		http.Error(w, "Internal error", http.StatusInternalServerError)
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
	wh, err := h.store.ListWorkingHours(ctx)
	if err != nil {
		return nil, err
	}

	dayDate := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, h.timezone)
	weekday := int(dayDate.Weekday())

	var todayWH *model.WorkingHours
	for _, w := range wh {
		if w.DayOfWeek == weekday && w.Active {
			todayWH = &w
			break
		}
	}
	if todayWH == nil {
		return nil, nil // not a working day
	}

	startH, startM := parseTime(todayWH.StartTime)
	endH, endM := parseTime(todayWH.EndTime)
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

	// Get calendar events from ALL calendars (real free/busy)
	var calEvents []calendarapi.Event
	if h.cal.Enabled() {
		calEvents, err = h.cal.GetEvents(ctx, "", dayStart, dayEnd)
		if err != nil {
			log.Printf("calendar api error (non-fatal): %v", err)
		}
	}

	// Check if any blocking all-day event matches configured keywords
	blockKeywords := h.getBlockKeywords(ctx)
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

		// Check against bookings in our DB
		for _, b := range bookings {
			if t.Before(b.EndTime) && slotEnd.After(b.StartTime) {
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
				if t.Before(ev.End) && slotEnd.After(ev.Start) {
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

	endTime := startTime.Add(time.Duration(mt.DurationMin) * time.Minute)

	// Create calendar event if calendar is configured
	var calEventID string
	if h.cal.Enabled() && mt.CalendarID != "" {
		ev, err := h.cal.CreateEvent(r.Context(), calendarapi.CreateEventRequest{
			CalendarID:  mt.CalendarID,
			Title:       fmt.Sprintf("%s with %s", mt.Title, guestName),
			Start:       startTime.UTC().Format(time.RFC3339),
			End:         endTime.UTC().Format(time.RFC3339),
			Description: fmt.Sprintf("Booked via Book\nGuest: %s (%s)", guestName, guestEmail),
			Attendees: []calendarapi.Attendee{
				{Email: guestEmail, Name: guestName},
			},
		})
		if err != nil {
			log.Printf("calendar create error: %v", err)
			// Don't fail the booking — save it without calendar event
		} else {
			calEventID = ev.ID
		}
	}

	booking := &model.Booking{
		MeetingTypeID: mt.ID,
		GuestName:     guestName,
		GuestEmail:    guestEmail,
		StartTime:     startTime.UTC(),
		EndTime:       endTime.UTC(),
		Timezone:      guestTZ,
		Status:        "confirmed",
		CalendarEvent: calEventID,
	}

	if err := h.store.CreateBooking(r.Context(), booking); err != nil {
		http.Error(w, "Failed to create booking", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", fmt.Sprintf("/confirmation/%d", booking.ID))
	w.WriteHeader(http.StatusOK)
}

func (h *PublicHandler) confirmation(w http.ResponseWriter, r *http.Request) {
	h.render(w, "confirmation.html", map[string]any{
		"Title": "Booking Confirmed — Book",
		"ID":    chi.URLParam(r, "id"),
	})
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
