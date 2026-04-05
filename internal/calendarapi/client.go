package calendarapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type Event struct {
	ID          string     `json:"id"`
	CalendarID  string     `json:"calendar_id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	Attendees   []Attendee `json:"attendees,omitempty"`
}

type Attendee struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

type CreateEventRequest struct {
	CalendarID  string     `json:"calendar_id"`
	Title       string     `json:"title"`
	Start       string     `json:"start"`
	End         string     `json:"end"`
	Description string     `json:"description,omitempty"`
	Attendees   []Attendee `json:"attendees,omitempty"`
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Enabled returns true if the calendar API is configured.
func (c *Client) Enabled() bool {
	return c.baseURL != ""
}

// GetEvents returns events from a specific calendar for the given time range.
func (c *Client) GetEvents(ctx context.Context, calendarID string, start, end time.Time) ([]Event, error) {
	params := url.Values{
		"calendar_id": {calendarID},
		"start":       {start.UTC().Format(time.RFC3339)},
		"end":         {end.UTC().Format(time.RFC3339)},
	}

	var events []Event
	if err := c.get(ctx, "/api/events?"+params.Encode(), &events); err != nil {
		return nil, err
	}
	return events, nil
}

// CreateEvent creates a calendar event and returns the created event.
func (c *Client) CreateEvent(ctx context.Context, req CreateEventRequest) (*Event, error) {
	var ev Event
	if err := c.post(ctx, "/api/events", req, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// DeleteEvent deletes a calendar event.
func (c *Client) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	params := url.Values{
		"calendar_id": {calendarID},
		"event_id":    {eventID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/api/events?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendar api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.readError(resp)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendar api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func (c *Client) post(ctx context.Context, path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendar api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *Client) readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("calendar api %d: %s", resp.StatusCode, string(body))
}
