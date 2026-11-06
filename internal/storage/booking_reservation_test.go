package storage

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dzarlax/book/internal/model"
)

// Uses only a disposable test container named postgres and its dedicated DB.
func TestSyntheticReservationRace(t *testing.T) {
	raw := os.Getenv("BOOK_SYNTHETIC_POSTGRES_URL")
	if raw == "" {
		t.Skip("disposable PostgreSQL URL not provided")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "postgres" || u.Path != "/book_synthetic" {
		t.Fatal("requires isolated postgres/book_synthetic")
	}
	s, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate("../../migrations"); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
	ctx := context.Background()
	for _, slug := range []string{"test-one", "test-two"} {
		mt := &model.MeetingType{Slug: slug, Title: slug, DurationMin: 60, BufferMin: 10, Active: true, CalendarID: "google:original"}
		if err := s.CreateMeetingType(ctx, mt); err != nil {
			t.Fatal(err)
		}
	}
	types, err := s.ListMeetingTypes(ctx, true)
	if err != nil || len(types) != 2 {
		t.Fatalf("meeting types: %v", err)
	}
	start := time.Date(2027, 3, 28, 0, 15, 0, 0, time.UTC)
	bookings := []*model.Booking{{MeetingTypeID: types[0].ID, StartTime: start, EndTime: start.Add(time.Hour), CalendarIDSnapshot: "google:original"}, {MeetingTypeID: types[1].ID, StartTime: start, EndTime: start.Add(time.Hour), CalendarIDSnapshot: "google:original"}}
	errs := make([]error, len(bookings))
	var wg sync.WaitGroup
	for i := range bookings {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = s.ReserveBooking(ctx, bookings[i], time.UTC) }(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatal("overlapping reservations both succeeded")
			}
			winner = i
		} else if !errors.Is(err, ErrBookingConflict) {
			t.Fatal(err)
		}
	}
	if winner == -1 {
		t.Fatal("no reservation succeeded")
	}
	id := bookings[winner].ID
	if err := s.ConfirmBooking(ctx, id, ""); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("empty event id: %v", err)
	}
	if err := s.CancelBooking(ctx, id); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("cancel pending: %v", err)
	}
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetBookingsForDay(ctx, start.In(loc), types[1-winner].ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("blocking read: %v %d", err, len(rows))
	}
	if err := s.ConfirmBooking(ctx, id, "synthetic-event"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmBooking(ctx, id, "another-event"); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("repeat confirm: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE meeting_types SET calendar_id='google:changed' WHERE id=$1`, types[winner].ID); err != nil {
		t.Fatal(err)
	}
	stale := &model.Booking{MeetingTypeID: types[winner].ID, StartTime: start.Add(4 * time.Hour), EndTime: start.Add(5 * time.Hour), CalendarIDSnapshot: "google:original"}
	if err := s.ReserveBooking(ctx, stale, time.UTC); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("stale preflight accepted: %v", err)
	}
	cancellationResults := make([]error, 2)
	targets := make([]*CancellationTarget, 2)
	for i := range cancellationResults {
		wg.Add(1)
		go func(i int) { defer wg.Done(); targets[i], cancellationResults[i] = s.BeginCancellation(ctx, id) }(i)
	}
	wg.Wait()
	if !((cancellationResults[0] == nil && errors.Is(cancellationResults[1], ErrBookingStateConflict)) || (cancellationResults[1] == nil && errors.Is(cancellationResults[0], ErrBookingStateConflict))) {
		t.Fatalf("concurrent cancellation claims: %v", cancellationResults)
	}
	for _, target := range targets {
		if target != nil && (target.CalendarID != "google:original" || target.EventID != "synthetic-event") {
			t.Fatalf("wrong original target: %+v", target)
		}
	}
	if err := s.CancelBooking(ctx, id); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("repeat cancellation: %v", err)
	}
	rows, err = s.GetBookingsForDay(ctx, start.In(loc), types[winner].ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "cancelling" {
		t.Fatalf("cancelling did not block read: %v", err)
	}
	tooClose := &model.Booking{MeetingTypeID: types[1-winner].ID, StartTime: start.Add(65 * time.Minute), EndTime: start.Add(125 * time.Minute), CalendarIDSnapshot: "google:original"}
	if err := s.ReserveBooking(ctx, tooClose, time.UTC); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("existing buffer not respected: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE meeting_types SET max_per_day=1 WHERE id=$1`, types[winner].ID); err != nil {
		t.Fatal(err)
	}
	tooMany := &model.Booking{MeetingTypeID: types[winner].ID, StartTime: start.Add(3 * time.Hour), EndTime: start.Add(4 * time.Hour), CalendarIDSnapshot: "google:changed"}
	if err := s.ReserveBooking(ctx, tooMany, time.UTC); !errors.Is(err, ErrBookingLimit) {
		t.Fatalf("daily limit not respected: %v", err)
	}
	localDay := time.Date(2027, 3, 28, 0, 0, 0, 0, loc)
	crossing := &model.Booking{MeetingTypeID: types[winner].ID, StartTime: localDay.Add(-30 * time.Minute), EndTime: localDay.Add(30 * time.Minute), Status: "confirmed", CalendarEvent: "legacy-event"}
	if err := s.CreateBooking(ctx, crossing); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCancellation(ctx, crossing.ID); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("legacy snapshot missing was accepted: %v", err)
	}
	legacy, err := s.GetBooking(ctx, crossing.ID)
	if err != nil || legacy.Status != "confirmed" || legacy.CalendarIDSnapshot != "" {
		t.Fatalf("legacy changed after refused cancel: %+v %v", legacy, err)
	}
	outside := &model.Booking{MeetingTypeID: types[winner].ID, StartTime: localDay.AddDate(0, 0, 1).Add(15 * time.Minute), EndTime: localDay.AddDate(0, 0, 1).Add(time.Hour), Status: "confirmed"}
	if err := s.CreateBooking(ctx, outside); err != nil {
		t.Fatal(err)
	}
	withinBuffer := &model.Booking{MeetingTypeID: types[winner].ID, StartTime: localDay.AddDate(0, 0, 1).Add(5 * time.Minute), EndTime: localDay.AddDate(0, 0, 1).Add(time.Hour), Status: "confirmed"}
	if err := s.CreateBooking(ctx, withinBuffer); err != nil {
		t.Fatal(err)
	}
	rows, err = s.GetBookingsForDay(ctx, localDay, types[winner].ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("DST/cross-day overlap: %v count=%d", err, len(rows))
	}
	if err := s.FinishCancellation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCancellation(ctx, id); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("repeat finish: %v", err)
	}
	rows, err = s.GetBookingsForDay(ctx, localDay, types[winner].ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("confirmed cancellation still blocks: %v count=%d", err, len(rows))
	}
}

func TestSyntheticSnapshotMigrationPreservesLegacy(t *testing.T) {
	raw := os.Getenv("BOOK_SYNTHETIC_POSTGRES_URL")
	if raw == "" {
		t.Skip("disposable PostgreSQL URL not provided")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "postgres" || u.Path != "/book_synthetic" {
		t.Fatal("requires isolated postgres/book_synthetic")
	}
	admin, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.db.Exec(`CREATE SCHEMA synthetic_legacy_upgrade`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.db.Exec(`DROP SCHEMA synthetic_legacy_upgrade CASCADE`) }()
	q := u.Query()
	q.Set("search_path", "synthetic_legacy_upgrade")
	u.RawQuery = q.Encode()
	s, err := New(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"001_init.sql", "002_meeting_type_hours.sql"} {
		body, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO meeting_types (id,slug,title,calendar_id) VALUES (1,'legacy','legacy','google:synthetic'); INSERT INTO bookings (id,meeting_type_id,guest_name,guest_email,start_time,end_time,status,calendar_event_id) VALUES (1,1,'synthetic','synthetic',now(),now()+interval '1 hour','confirmed','legacy-event')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Migrate("../../migrations"); err != nil {
			t.Fatalf("migration pass %d: %v", i, err)
		}
	}
	var isNull bool
	if err := s.db.QueryRow(`SELECT calendar_id_snapshot IS NULL FROM bookings WHERE id=1`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("legacy snapshot changed: null=%v err=%v", isNull, err)
	}
	if _, err := s.BeginCancellation(context.Background(), 1); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("legacy cancellation permitted: %v", err)
	}
	booking, err := s.GetBooking(context.Background(), 1)
	if err != nil || booking == nil || booking.Status != "confirmed" {
		t.Fatalf("legacy state changed: %+v %v", booking, err)
	}
}
