package model

import "time"

type MeetingType struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	DurationMin int       `json:"duration_min"`
	BufferMin   int       `json:"buffer_min"`
	MaxPerDay   int       `json:"max_per_day"`
	CalendarID  string    `json:"calendar_id"` // calendar-mcp calendar ID (e.g. "google:primary")
	VideoCall   bool      `json:"video_call"`  // auto-create Google Meet / Teams link
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

type WorkingHours struct {
	ID        int64  `json:"id"`
	DayOfWeek int    `json:"day_of_week"` // 0=Sunday, 6=Saturday
	StartTime string `json:"start_time"`  // "09:00"
	EndTime   string `json:"end_time"`    // "17:00"
	Active    bool   `json:"active"`
}

type Booking struct {
	ID               int64     `json:"id"`
	MeetingTypeID    int64     `json:"meeting_type_id"`
	MeetingTypeTitle string    `json:"meeting_type_title"` // from JOIN, not in DB
	GuestName        string    `json:"guest_name"`
	GuestEmail       string    `json:"guest_email"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	Timezone         string    `json:"timezone"`
	Status           string    `json:"status"` // confirmed, cancelled
	CalendarEvent    string    `json:"calendar_event_id"`
	CreatedAt        time.Time `json:"created_at"`
}

type TimeSlot struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Available bool      `json:"available"`
}
