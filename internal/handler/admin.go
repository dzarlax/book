package handler

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/model"
	"github.com/dzarlax/book/internal/storage"
)

type AdminHandler struct {
	store    *storage.Storage
	cal      *calendarapi.Client
	tmpl     *template.Template
	timezone *time.Location
}

func NewAdminHandler(store *storage.Storage, cal *calendarapi.Client, tmpl *template.Template, tz *time.Location) *AdminHandler {
	return &AdminHandler{store: store, cal: cal, tmpl: tmpl, timezone: tz}
}

type calendarGroup struct {
	Provider  string
	Calendars []calendarOption
}

type calendarOption struct {
	ID   string
	Name string
}

func (h *AdminHandler) fetchCalendarGroups(r *http.Request) []calendarGroup {
	if !h.cal.Enabled() {
		return nil
	}
	cals, err := h.cal.ListCalendars(r.Context())
	if err != nil {
		log.Printf("fetch calendars: %v", err)
		return nil
	}
	grouped := make(map[string][]calendarOption)
	var order []string
	for _, c := range cals {
		if c.ReadOnly {
			continue
		}
		if _, ok := grouped[c.Provider]; !ok {
			order = append(order, c.Provider)
		}
		grouped[c.Provider] = append(grouped[c.Provider], calendarOption{ID: c.ID, Name: c.Name})
	}
	groups := make([]calendarGroup, len(order))
	for i, p := range order {
		groups[i] = calendarGroup{Provider: providerLabel(p), Calendars: grouped[p]}
	}
	return groups
}

func providerLabel(p string) string {
	switch p {
	case "google":
		return "Google Calendar"
	case "microsoft":
		return "Microsoft 365"
	case "apple":
		return "Apple iCloud"
	default:
		return p
	}
}

func (h *AdminHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.dashboard)
	r.Get("/types", h.listTypes)
	r.Get("/types/new", h.newTypeForm)
	r.Post("/types", h.createType)
	r.Get("/types/{id}/edit", h.editTypeForm)
	r.Post("/types/{id}", h.updateType)
	r.Delete("/types/{id}", h.deleteType)
	r.Get("/hours", h.workingHours)
	r.Post("/hours", h.saveWorkingHours)
	r.Get("/bookings", h.listBookings)
	r.Post("/bookings/{id}/cancel", h.cancelBooking)
	r.Get("/settings", h.settings)
	r.Post("/settings", h.saveSettings)
	return r
}

func (h *AdminHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	types, _ := h.store.ListMeetingTypes(r.Context(), false)
	now := time.Now().In(h.timezone)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.timezone)
	to := from.Add(7 * 24 * time.Hour)
	allBookings, _ := h.store.ListBookings(r.Context(), from, to)
	var upcoming []model.Booking
	for _, b := range allBookings {
		if b.Status == "confirmed" {
			upcoming = append(upcoming, b)
		}
	}

	h.render(w, "admin_dashboard.html", map[string]any{
		"Title":          "Admin — Book",
		"ContainerClass": " container--wide",
		"Types":          types,
		"Bookings":       upcoming,
	})
}

func (h *AdminHandler) listTypes(w http.ResponseWriter, r *http.Request) {
	types, err := h.store.ListMeetingTypes(r.Context(), false)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_types.html", map[string]any{"Title": "Meeting Types — Admin", "ContainerClass": " container--wide", "Types": types})
}

func (h *AdminHandler) newTypeForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, "admin_type_form.html", map[string]any{
		"Title":          "New Meeting Type — Admin",
		"ContainerClass": " container--wide",
		"Meeting":        &model.MeetingType{DurationMin: 30, BufferMin: 10, MaxPerDay: 8, Active: true},
		"IsNew":          true,
		"Calendars":      h.fetchCalendarGroups(r),
	})
}

func (h *AdminHandler) createType(w http.ResponseWriter, r *http.Request) {
	mt := h.parseMeetingTypeForm(r)
	if err := h.store.CreateMeetingType(r.Context(), mt); err != nil {
		http.Error(w, "Failed to create", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/types")
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) editTypeForm(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	types, _ := h.store.ListMeetingTypes(r.Context(), false)
	var mt *model.MeetingType
	for _, t := range types {
		if t.ID == id {
			mt = &t
			break
		}
	}
	if mt == nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, "admin_type_form.html", map[string]any{"Title": "Edit — Admin", "ContainerClass": " container--wide", "Meeting": mt, "IsNew": false, "Calendars": h.fetchCalendarGroups(r)})
}

func (h *AdminHandler) updateType(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	mt := h.parseMeetingTypeForm(r)
	mt.ID = id
	if err := h.store.UpdateMeetingType(r.Context(), mt); err != nil {
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/types")
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) deleteType(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.store.DeleteMeetingType(r.Context(), id); err != nil {
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) workingHours(w http.ResponseWriter, r *http.Request) {
	hours, err := h.store.ListWorkingHours(r.Context())
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	// Fill all 7 days, keyed by day_of_week
	byDay := make(map[int]model.WorkingHours, 7)
	for i := 0; i < 7; i++ {
		byDay[i] = model.WorkingHours{DayOfWeek: i, StartTime: "09:00", EndTime: "17:00"}
	}
	for _, wh := range hours {
		byDay[wh.DayOfWeek] = wh
	}
	// Order: Mon(1)..Sun(0)
	order := []int{1, 2, 3, 4, 5, 6, 0}
	days := make([]model.WorkingHours, 7)
	for i, d := range order {
		days[i] = byDay[d]
	}
	h.render(w, "admin_hours.html", map[string]any{"Title": "Working Hours — Admin", "ContainerClass": " container--wide", "Days": days})
}

func (h *AdminHandler) saveWorkingHours(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	for i := 0; i < 7; i++ {
		prefix := strconv.Itoa(i)
		active := r.FormValue("active_"+prefix) == "on"
		wh := &model.WorkingHours{
			DayOfWeek: i,
			StartTime: r.FormValue("start_" + prefix),
			EndTime:   r.FormValue("end_" + prefix),
			Active:    active,
		}
		if err := h.store.UpsertWorkingHours(r.Context(), wh); err != nil {
			http.Error(w, "Failed to save", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("HX-Redirect", "/admin/hours")
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) listBookings(w http.ResponseWriter, r *http.Request) {
	now := time.Now().In(h.timezone)
	from := now.Add(-30 * 24 * time.Hour)
	to := now.Add(60 * 24 * time.Hour)
	bookings, err := h.store.ListBookings(r.Context(), from, to)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_bookings.html", map[string]any{"Title": "Bookings — Admin", "ContainerClass": " container--wide", "Bookings": bookings, "Timezone": h.timezone.String()})
}

func (h *AdminHandler) cancelBooking(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.store.CancelBooking(r.Context(), id); err != nil {
		http.Error(w, "Failed to cancel", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) settings(w http.ResponseWriter, r *http.Request) {
	keywords, _ := h.store.GetSetting(r.Context(), "block_allday_keywords")
	h.render(w, "admin_settings.html", map[string]any{
		"Title":          "Settings — Admin",
		"ContainerClass": " container--wide",
		"BlockKeywords":  keywords,
	})
}

func (h *AdminHandler) saveSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if err := h.store.SetSetting(r.Context(), "block_allday_keywords", r.FormValue("block_allday_keywords")); err != nil {
		http.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *AdminHandler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (h *AdminHandler) parseMeetingTypeForm(r *http.Request) *model.MeetingType {
	r.ParseForm()
	duration, _ := strconv.Atoi(r.FormValue("duration_min"))
	buffer, _ := strconv.Atoi(r.FormValue("buffer_min"))
	maxPerDay, _ := strconv.Atoi(r.FormValue("max_per_day"))
	return &model.MeetingType{
		Slug:        r.FormValue("slug"),
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		DurationMin: duration,
		BufferMin:   buffer,
		MaxPerDay:   maxPerDay,
		CalendarID:  r.FormValue("calendar_id"),
		VideoCall:   r.FormValue("video_call") == "on",
		Active:      r.FormValue("active") == "on",
	}
}
