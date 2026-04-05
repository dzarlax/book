package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/dzarlax/book/internal/model"
)

type Storage struct {
	db *sql.DB
}

func New(databaseURL string) (*Storage, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Storage{db: db}, nil
}

func (s *Storage) Migrate(migrationsDir string) error {
	data, err := os.ReadFile(migrationsDir + "/001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	_, err = s.db.Exec(string(data))
	if err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	return nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}

// --- Meeting Types ---

func (s *Storage) ListMeetingTypes(ctx context.Context, onlyActive bool) ([]model.MeetingType, error) {
	query := "SELECT id, slug, title, description, duration_min, buffer_min, max_per_day, calendar_id, video_call, active, created_at FROM meeting_types"
	if onlyActive {
		query += " WHERE active = true"
	}
	query += " ORDER BY title"

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var types []model.MeetingType
	for rows.Next() {
		var mt model.MeetingType
		if err := rows.Scan(&mt.ID, &mt.Slug, &mt.Title, &mt.Description, &mt.DurationMin, &mt.BufferMin, &mt.MaxPerDay, &mt.CalendarID, &mt.VideoCall, &mt.Active, &mt.CreatedAt); err != nil {
			return nil, err
		}
		types = append(types, mt)
	}
	return types, rows.Err()
}

func (s *Storage) GetMeetingType(ctx context.Context, slug string) (*model.MeetingType, error) {
	var mt model.MeetingType
	err := s.db.QueryRowContext(ctx,
		"SELECT id, slug, title, description, duration_min, buffer_min, max_per_day, calendar_id, video_call, active, created_at FROM meeting_types WHERE slug = $1",
		slug,
	).Scan(&mt.ID, &mt.Slug, &mt.Title, &mt.Description, &mt.DurationMin, &mt.BufferMin, &mt.MaxPerDay, &mt.CalendarID, &mt.VideoCall, &mt.Active, &mt.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mt, nil
}

func (s *Storage) CreateMeetingType(ctx context.Context, mt *model.MeetingType) error {
	return s.db.QueryRowContext(ctx,
		`INSERT INTO meeting_types (slug, title, description, duration_min, buffer_min, max_per_day, calendar_id, video_call, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at`,
		mt.Slug, mt.Title, mt.Description, mt.DurationMin, mt.BufferMin, mt.MaxPerDay, mt.CalendarID, mt.VideoCall, mt.Active,
	).Scan(&mt.ID, &mt.CreatedAt)
}

func (s *Storage) UpdateMeetingType(ctx context.Context, mt *model.MeetingType) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meeting_types SET slug=$1, title=$2, description=$3, duration_min=$4, buffer_min=$5, max_per_day=$6, calendar_id=$7, video_call=$8, active=$9 WHERE id=$10`,
		mt.Slug, mt.Title, mt.Description, mt.DurationMin, mt.BufferMin, mt.MaxPerDay, mt.CalendarID, mt.VideoCall, mt.Active, mt.ID,
	)
	return err
}

func (s *Storage) DeleteMeetingType(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM meeting_types WHERE id = $1", id)
	return err
}

// --- Working Hours ---

func (s *Storage) ListWorkingHours(ctx context.Context) ([]model.WorkingHours, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, day_of_week, start_time, end_time, active FROM working_hours ORDER BY day_of_week")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hours []model.WorkingHours
	for rows.Next() {
		var wh model.WorkingHours
		if err := rows.Scan(&wh.ID, &wh.DayOfWeek, &wh.StartTime, &wh.EndTime, &wh.Active); err != nil {
			return nil, err
		}
		hours = append(hours, wh)
	}
	return hours, rows.Err()
}

func (s *Storage) UpsertWorkingHours(ctx context.Context, wh *model.WorkingHours) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO working_hours (day_of_week, start_time, end_time, active)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (day_of_week) DO UPDATE SET start_time=$2, end_time=$3, active=$4`,
		wh.DayOfWeek, wh.StartTime, wh.EndTime, wh.Active,
	)
	return err
}

// --- Bookings ---

func (s *Storage) CreateBooking(ctx context.Context, b *model.Booking) error {
	return s.db.QueryRowContext(ctx,
		`INSERT INTO bookings (meeting_type_id, guest_name, guest_email, start_time, end_time, timezone, status, calendar_event_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		b.MeetingTypeID, b.GuestName, b.GuestEmail, b.StartTime, b.EndTime, b.Timezone, b.Status, b.CalendarEvent,
	).Scan(&b.ID, &b.CreatedAt)
}

func (s *Storage) ListBookings(ctx context.Context, from, to time.Time) ([]model.Booking, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.id, b.meeting_type_id, COALESCE(mt.title, ''), b.guest_name, b.guest_email, b.start_time, b.end_time, b.timezone, b.status, b.calendar_event_id, b.created_at
		 FROM bookings b LEFT JOIN meeting_types mt ON mt.id = b.meeting_type_id
		 WHERE b.start_time >= $1 AND b.start_time < $2 ORDER BY b.start_time`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bookings []model.Booking
	for rows.Next() {
		var b model.Booking
		if err := rows.Scan(&b.ID, &b.MeetingTypeID, &b.MeetingTypeTitle, &b.GuestName, &b.GuestEmail, &b.StartTime, &b.EndTime, &b.Timezone, &b.Status, &b.CalendarEvent, &b.CreatedAt); err != nil {
			return nil, err
		}
		bookings = append(bookings, b)
	}
	return bookings, rows.Err()
}

func (s *Storage) GetBookingsForDay(ctx context.Context, date time.Time, meetingTypeID int64) ([]model.Booking, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, meeting_type_id, guest_name, guest_email, start_time, end_time, timezone, status, calendar_event_id, created_at
		 FROM bookings WHERE meeting_type_id = $1 AND start_time >= $2 AND start_time < $3 AND status = 'confirmed' ORDER BY start_time`,
		meetingTypeID, startOfDay, endOfDay,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bookings []model.Booking
	for rows.Next() {
		var b model.Booking
		if err := rows.Scan(&b.ID, &b.MeetingTypeID, &b.GuestName, &b.GuestEmail, &b.StartTime, &b.EndTime, &b.Timezone, &b.Status, &b.CalendarEvent, &b.CreatedAt); err != nil {
			return nil, err
		}
		bookings = append(bookings, b)
	}
	return bookings, rows.Err()
}

func (s *Storage) GetBooking(ctx context.Context, id int64) (*model.Booking, error) {
	var b model.Booking
	err := s.db.QueryRowContext(ctx,
		`SELECT b.id, b.meeting_type_id, COALESCE(mt.title, ''), b.guest_name, b.guest_email, b.start_time, b.end_time, b.timezone, b.status, b.calendar_event_id, b.created_at
		 FROM bookings b LEFT JOIN meeting_types mt ON mt.id = b.meeting_type_id WHERE b.id = $1`, id,
	).Scan(&b.ID, &b.MeetingTypeID, &b.MeetingTypeTitle, &b.GuestName, &b.GuestEmail, &b.StartTime, &b.EndTime, &b.Timezone, &b.Status, &b.CalendarEvent, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

func (s *Storage) GetMeetingTypeByID(ctx context.Context, id int64) (*model.MeetingType, error) {
	var mt model.MeetingType
	err := s.db.QueryRowContext(ctx,
		"SELECT id, slug, title, description, duration_min, buffer_min, max_per_day, calendar_id, video_call, active, created_at FROM meeting_types WHERE id = $1", id,
	).Scan(&mt.ID, &mt.Slug, &mt.Title, &mt.Description, &mt.DurationMin, &mt.BufferMin, &mt.MaxPerDay, &mt.CalendarID, &mt.VideoCall, &mt.Active, &mt.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &mt, err
}

func (s *Storage) CancelBooking(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE bookings SET status = 'cancelled' WHERE id = $1", id)
	return err
}

func (s *Storage) CountBookingsForDay(ctx context.Context, date time.Time, meetingTypeID int64) (int, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM bookings WHERE meeting_type_id = $1 AND start_time >= $2 AND start_time < $3 AND status = 'confirmed'",
		meetingTypeID, startOfDay, endOfDay,
	).Scan(&count)
	return count, err
}

// --- Settings ---

func (s *Storage) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (s *Storage) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = $2`,
		key, value,
	)
	return err
}
