package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dzarlax/book/internal/model"
)

var (
	ErrBookingConflict      = errors.New("booking overlaps an existing reservation")
	ErrBookingStateConflict = errors.New("booking is not in the required state")
	ErrBookingLimit         = errors.New("meeting type daily limit reached")
)

// ReserveBooking commits a durable pending booking before any provider call.
// Any error, including an ambiguous commit result, forbids provider dispatch.
func (s *Storage) ReserveBooking(ctx context.Context, b *model.Booking, hostTZ *time.Location) error {
	if b == nil || hostTZ == nil || b.MeetingTypeID <= 0 || strings.TrimSpace(b.CalendarIDSnapshot) == "" || b.StartTime.IsZero() || !b.EndTime.After(b.StartTime) {
		return errors.New("invalid booking reservation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A global transaction-scoped lock serializes every Book reservation,
	// including reservations of different meeting types.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1128353611, 1112493907)`); err != nil {
		return err
	}
	if b.ExpectedSettings == nil {
		return ErrBookingStateConflict
	}
	currentSettings, err := readBookingSettings(ctx, tx)
	if err != nil {
		return err
	}
	if currentSettings != *b.ExpectedSettings {
		return ErrBookingStateConflict
	}
	var duration, buffer, maximum int
	var active bool
	var calendarID string
	if err = tx.QueryRowContext(ctx, `SELECT duration_min, buffer_min, max_per_day, active, calendar_id FROM meeting_types WHERE id = $1 FOR SHARE`, b.MeetingTypeID).Scan(&duration, &buffer, &maximum, &active, &calendarID); err != nil {
		return err
	}
	if !active || calendarID == "" || calendarID != b.CalendarIDSnapshot || duration <= 0 || buffer < 0 || buffer != b.ExpectedBufferMin || b.EndTime.Sub(b.StartTime) != time.Duration(duration)*time.Minute {
		return ErrBookingStateConflict
	}
	local := b.StartTime.In(hostTZ)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, hostTZ)
	dayEnd := dayStart.AddDate(0, 0, 1)
	if maximum > 0 {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM bookings WHERE meeting_type_id = $1 AND start_time >= $2 AND start_time < $3 AND status IN ('confirmed', 'pending', 'unknown', 'dispatching', 'cancelling')`, b.MeetingTypeID, dayStart, dayEnd).Scan(&count); err != nil {
			return err
		}
		if count >= maximum {
			return ErrBookingLimit
		}
	}
	var overlaps bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bookings b JOIN meeting_types mt ON mt.id = b.meeting_type_id
		WHERE b.status IN ('confirmed', 'pending', 'unknown', 'dispatching', 'cancelling')
		AND b.start_time < $2::timestamptz + make_interval(mins => $3)
		AND b.end_time + make_interval(mins => greatest(mt.buffer_min, 0)) > $1::timestamptz)`, b.StartTime, b.EndTime, buffer).Scan(&overlaps); err != nil {
		return err
	}
	if overlaps {
		return ErrBookingConflict
	}
	var id int64
	var createdAt = b.CreatedAt
	err = tx.QueryRowContext(ctx, `INSERT INTO bookings (meeting_type_id, guest_name, guest_email, start_time, end_time, timezone, status, calendar_event_id, calendar_id_snapshot)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', '', $7) RETURNING id, created_at`,
		b.MeetingTypeID, b.GuestName, b.GuestEmail, b.StartTime, b.EndTime, b.Timezone, calendarID).Scan(&id, &createdAt)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit booking reservation: %w", err)
	}
	b.ID, b.CreatedAt, b.Status, b.CalendarEvent = id, createdAt, "pending", ""
	b.CalendarIDSnapshot = calendarID
	return nil
}

// ConfirmBooking changes only a pending reservation after provider success.
func (s *Storage) ConfirmBooking(ctx context.Context, bookingID int64, eventID string) error {
	if bookingID <= 0 || strings.TrimSpace(eventID) == "" {
		return ErrBookingStateConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE bookings SET status = 'confirmed', calendar_event_id = $2 WHERE id = $1 AND status = 'pending'`, bookingID, eventID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBookingStateConflict
	}
	return nil
}

// BeginCancellation marks a confirmed booking before provider deletion.
// On an ambiguous update result, callers must not call the provider.
type CancellationTarget struct {
	CalendarID string
	EventID    string
}

func (s *Storage) BeginCancellation(ctx context.Context, bookingID int64) (*CancellationTarget, error) {
	if bookingID <= 0 {
		return nil, ErrBookingStateConflict
	}
	var target CancellationTarget
	err := s.db.QueryRowContext(ctx, `UPDATE bookings SET status='cancelling'
		WHERE id=$1 AND status='confirmed' AND calendar_id_snapshot IS NOT NULL
		AND btrim(calendar_id_snapshot) <> '' AND btrim(calendar_event_id) <> ''
		RETURNING calendar_id_snapshot, calendar_event_id`, bookingID).Scan(&target.CalendarID, &target.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBookingStateConflict
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

// FinishCancellation releases the slot only after provider deletion is confirmed.
func (s *Storage) FinishCancellation(ctx context.Context, bookingID int64) error {
	return s.transitionBooking(ctx, bookingID, "cancelling", "cancelled")
}

func (s *Storage) transitionBooking(ctx context.Context, bookingID int64, from, to string) error {
	if bookingID <= 0 {
		return ErrBookingStateConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE bookings SET status=$3 WHERE id=$1 AND status=$2`, bookingID, from, to)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBookingStateConflict
	}
	return nil
}
