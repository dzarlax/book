package calendarapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func Providers() []string { return []string{"google", "microsoft", "apple"} }

// CalendarProvider accepts either supported account-scoped wire representation.
// It never resolves an ambiguous legacy alias to an account.
func CalendarProvider(id string) (string, bool) {
	if strings.TrimSpace(id) != id {
		return "", false
	}
	route, raw, ok := strings.Cut(id, ":")
	if !ok {
		return "", false
	}
	provider, account, routed := strings.Cut(route, "@")
	if !routed {
		account, raw, routed = strings.Cut(raw, ":")
	}
	if !routed || account == "" || raw == "" || raw == "primary" {
		return "", false
	}
	for _, p := range Providers() {
		if p == provider {
			return p, true
		}
	}
	return "", false
}

func (c *Client) ListProviderCalendars(ctx context.Context, provider string) ([]Calendar, error) {
	valid := false
	for _, p := range Providers() {
		if provider == p {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("invalid calendar provider")
	}
	var page struct {
		Provider  string          `json:"provider"`
		Complete  *bool           `json:"complete"`
		Calendars json.RawMessage `json:"calendars"`
	}
	if err := c.get(ctx, "/api/v2/availability/calendars?"+url.Values{"provider": {provider}}.Encode(), &page); err != nil {
		return nil, err
	}
	if page.Provider != provider || page.Complete == nil || !*page.Complete || len(page.Calendars) == 0 || string(page.Calendars) == "null" {
		return nil, fmt.Errorf("calendar catalog is incomplete")
	}
	var calendars []Calendar
	if err := json.Unmarshal(page.Calendars, &calendars); err != nil {
		return nil, fmt.Errorf("invalid calendar catalog")
	}
	seen := map[string]bool{}
	for _, calendar := range calendars {
		p, ok := CalendarProvider(calendar.ID)
		if !ok || p != provider || calendar.Provider != provider || seen[calendar.ID] {
			return nil, fmt.Errorf("calendar catalog identity is invalid")
		}
		seen[calendar.ID] = true
	}
	return calendars, nil
}

// CheckBookingTarget proves the exact target's current typed create capability.
// The legacy Microsoft guest-write contract remains deliberately unsupported.
func (c *Client) CheckBookingTarget(ctx context.Context, id string, video bool) error {
	provider, ok := CalendarProvider(id)
	if !ok || provider != "google" {
		return fmt.Errorf("booking target is unsupported or ambiguous")
	}
	var caps struct {
		Provider   string `json:"provider"`
		CalendarID string `json:"calendar_id"`
		ReadOnly   *bool  `json:"read_only"`
		Operations struct {
			Create *bool `json:"create"`
		} `json:"operations"`
		Fields struct {
			Conferencing bool `json:"conferencing"`
		} `json:"fields"`
		Notifications []string `json:"notification_policies"`
	}
	if err := c.get(ctx, "/api/v2/capabilities?"+url.Values{"calendar_id": {id}}.Encode(), &caps); err != nil {
		return err
	}
	none := false
	for _, p := range caps.Notifications {
		if p == "none" {
			none = true
		}
	}
	if caps.Provider != provider || caps.CalendarID != id || caps.ReadOnly == nil || *caps.ReadOnly || caps.Operations.Create == nil || !*caps.Operations.Create || !none || (video && !caps.Fields.Conferencing) {
		return fmt.Errorf("booking target creation capability unavailable")
	}
	return nil
}
