package handler

import (
	"encoding/json"
	"github.com/dzarlax/book/internal/calendarapi"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Existing event/booking fixtures keep their catalog data in a legacy-shaped
// array. This adapter serves that data through the new provider-scoped wire
// contract while preserving their explicit HTTP failure scenarios.
func newCalendarFixtureServer(inner http.Handler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/capabilities" {
			id := r.URL.Query().Get("calendar_id")
			provider, ok := calendarapi.CalendarProvider(id)
			if !ok {
				http.Error(w, "invalid", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"provider": provider, "calendar_id": id, "read_only": false, "operations": map[string]bool{"create": true}, "fields": map[string]bool{"conferencing": true}, "notification_policies": []string{"none"}})
			return
		}
		if r.URL.Path == "/api/v2/availability/calendars" {
			clone := r.Clone(r.Context())
			u := *r.URL
			u.Path = "/api/calendars"
			clone.URL = &u
			response := httptest.NewRecorder()
			inner.ServeHTTP(response, clone)
			if response.Code != 200 {
				w.WriteHeader(response.Code)
				io.Copy(w, response.Body)
				return
			}
			var old []calendarapi.Calendar
			if json.Unmarshal(response.Body.Bytes(), &old) != nil {
				http.Error(w, "invalid fixture", 500)
				return
			}
			provider := r.URL.Query().Get("provider")
			items := []calendarapi.Calendar{}
			for _, calendar := range old {
				if calendar.Provider == "" {
					route, _, _ := strings.Cut(calendar.ID, ":")
					calendar.Provider, _, _ = strings.Cut(route, "@")
				}
				if calendar.Provider == provider {
					items = append(items, calendar)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"provider": provider, "complete": true, "calendars": items})
			return
		}
		inner.ServeHTTP(w, r)
	}))
}
