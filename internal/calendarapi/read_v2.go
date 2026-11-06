package calendarapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type eventTimeV2 struct {
	Date     string `json:"date"`
	DateTime string `json:"date_time"`
	TimeZone string `json:"time_zone"`
}

func (v eventTimeV2) instant() (time.Time, bool, error) {
	if (v.Date == "") == (v.DateTime == "") {
		return time.Time{}, false, fmt.Errorf("calendar time representation is invalid")
	}
	if v.Date != "" {
		if v.TimeZone != "" {
			return time.Time{}, false, fmt.Errorf("all-day time contains a timezone")
		}
		t, err := time.Parse("2006-01-02", v.Date)
		return t, true, err
	}
	if v.TimeZone == "Local" {
		return time.Time{}, false, fmt.Errorf("calendar timezone is missing")
	}
	if v.TimeZone != "" {
		if _, err := time.LoadLocation(v.TimeZone); err != nil {
			return time.Time{}, false, err
		}
	}
	t, err := time.Parse(time.RFC3339, v.DateTime)
	return t, false, err
}

type eventV2 struct {
	ID           string      `json:"id"`
	CalendarID   string      `json:"calendar_id"`
	Provider     string      `json:"provider"`
	Title        string      `json:"title"`
	Start        eventTimeV2 `json:"start"`
	End          eventTimeV2 `json:"end"`
	Recurrence   []string    `json:"recurrence"`
	InstanceKind string      `json:"instance_kind"`
}
type sourceV2 struct {
	CalendarID string          `json:"calendar_id"`
	Provider   string          `json:"provider"`
	Complete   *bool           `json:"complete"`
	Error      json.RawMessage `json:"error"`
}
type eventPageV2 struct {
	Items         json.RawMessage `json:"items"`
	Complete      *bool           `json:"complete"`
	Sources       []sourceV2      `json:"sources"`
	NextPageToken string          `json:"next_page_token"`
}

// getExpandedEvents requires a complete direct-calendar V2 response. It never
// falls back to a legacy fan-out or returns a successfully read partial prefix.
func (c *Client) getExpandedEvents(ctx context.Context, calendarID string, start, end time.Time) ([]Event, error) {
	route, _, hasRoute := strings.Cut(calendarID, ":")
	expectedProvider, _, _ := strings.Cut(route, "@")
	if !hasRoute || (expectedProvider != "google" && expectedProvider != "microsoft" && expectedProvider != "apple") || strings.TrimSpace(calendarID) != calendarID || !end.After(start) {
		return nil, fmt.Errorf("exact calendar and valid range are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	params := url.Values{"calendar_id": {calendarID}, "start": {start.UTC().Format(time.RFC3339)}, "end": {end.UTC().Format(time.RFC3339)}, "view": {"expanded"}, "max_results": {"1000"}}
	result := []Event{}
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page eventPageV2
		if err := c.get(ctx, "/api/v2/availability/events?"+params.Encode(), &page); err != nil {
			return nil, err
		}
		if page.Complete == nil || !*page.Complete || len(page.Items) == 0 || len(page.Sources) == 0 {
			return nil, fmt.Errorf("calendar page is incomplete")
		}
		provider := ""
		for _, source := range page.Sources {
			if source.CalendarID != calendarID || source.Provider != expectedProvider || source.Complete == nil || !*source.Complete || (len(source.Error) > 0 && string(source.Error) != "null") {
				return nil, fmt.Errorf("calendar source is incomplete or mismatched")
			}
			if provider != "" && provider != source.Provider {
				return nil, fmt.Errorf("calendar provider is inconsistent")
			}
			provider = source.Provider
		}
		var items []eventV2
		// Explicit null is the server's existing representation of an empty slice;
		// missing items was rejected above. Complete/source evidence is mandatory.
		if err := json.Unmarshal(page.Items, &items); err != nil {
			return nil, fmt.Errorf("invalid calendar items: %w", err)
		}
		for _, item := range items {
			// Direct expanded provider reads normalize standalone Microsoft events to
			// empty kind; the remaining values are Calendar EventV2 instance kinds.
			switch item.InstanceKind {
			case "", "occurrence", "exception", "cancelled", "orphanOccurrence":
			default:
				return nil, fmt.Errorf("calendar event is not a recognized expanded instance")
			}
			if item.ID == "" || item.CalendarID != calendarID || item.Provider != provider || len(item.Recurrence) > 0 {
				return nil, fmt.Errorf("calendar event identity or expansion is invalid")
			}
			from, allDay, err := item.Start.instant()
			if err != nil {
				return nil, fmt.Errorf("invalid calendar event start: %w", err)
			}
			until, endAllDay, err := item.End.instant()
			if err != nil || from.IsZero() || until.IsZero() || allDay != endAllDay || !until.After(from) {
				return nil, fmt.Errorf("invalid calendar event range")
			}
			result = append(result, Event{ID: item.ID, CalendarID: item.CalendarID, Title: item.Title, Start: from, End: until, AllDay: allDay})
			if len(result) > 100000 {
				return nil, fmt.Errorf("calendar event limit exceeded")
			}
		}
		if page.NextPageToken == "" {
			return result, nil
		}
		if seen[page.NextPageToken] {
			return nil, fmt.Errorf("calendar cursor repeated")
		}
		seen[page.NextPageToken] = true
		params.Set("page_token", page.NextPageToken)
	}
	return nil, fmt.Errorf("calendar pagination limit exceeded")
}
