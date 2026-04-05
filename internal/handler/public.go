package handler

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type PublicHandler struct {
	store    *storage.Storage
	tmpl     *template.Template
	timezone *time.Location
}

func NewPublicHandler(store *storage.Storage, tmpl *template.Template, tz *time.Location) *PublicHandler {
	return &PublicHandler{store: store, tmpl: tmpl, timezone: tz}
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
	h.render(w, "index.html", map[string]any{"Types": types})
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
	stdCtx := ctx

	wh, err := h.store.ListWorkingHours(stdCtx)
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

	bookings, err := h.store.GetBookingsForDay(stdCtx, dayDate, mt.ID)
	if err != nil {
		return nil, err
	}

	count, err := h.store.CountBookingsForDay(stdCtx, dayDate, mt.ID)
	if err != nil {
		return nil, err
	}

	if mt.MaxPerDay > 0 && count >= mt.MaxPerDay {
		return nil, nil // daily limit reached
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
		for _, b := range bookings {
			if t.Before(b.EndTime) && slotEnd.After(b.StartTime) {
				available = false
				break
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

	booking := &model.Booking{
		MeetingTypeID: mt.ID,
		GuestName:     guestName,
		GuestEmail:    guestEmail,
		StartTime:     startTime.UTC(),
		EndTime:       endTime.UTC(),
		Timezone:      guestTZ,
		Status:        "confirmed",
	}

	if err := h.store.CreateBooking(r.Context(), booking); err != nil {
		http.Error(w, "Failed to create booking", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", fmt.Sprintf("/confirmation/%d", booking.ID))
	w.WriteHeader(http.StatusOK)
}

func (h *PublicHandler) confirmation(w http.ResponseWriter, r *http.Request) {
	// Simple confirmation page — booking ID in URL
	h.render(w, "confirmation.html", map[string]any{
		"ID": chi.URLParam(r, "id"),
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
