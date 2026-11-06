package storage

import (
	"context"
	"errors"
	"fmt"
	"github.com/dzarlax/book/internal/model"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestSyntheticSettingsReservationGuard(t *testing.T) {
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
	if err = s.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial, err := s.BookingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := initial
	defer func() {
		current, e := s.BookingSettings(ctx)
		if e == nil {
			_ = s.UpdateBookingSettings(ctx, current.Calendars, current.Keywords, original.Keywords, original.Calendars)
		}
	}()
	mt := &model.MeetingType{Slug: fmt.Sprintf("settings-guard-%d", time.Now().UnixNano()), Title: "Synthetic", DurationMin: 30, CalendarID: "google:acct:calendar", Active: true}
	if err = s.CreateMeetingType(ctx, mt); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2080, 1, 1, 10, 0, 0, 0, time.UTC)
	makeBooking := func() *model.Booking {
		return &model.Booking{MeetingTypeID: mt.ID, StartTime: start, EndTime: start.Add(30 * time.Minute), CalendarIDSnapshot: mt.CalendarID, ExpectedSettings: &initial}
	}
	if err = s.UpdateBookingSettings(ctx, initial.Calendars, initial.Keywords, "changed keywords", initial.Calendars); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveBooking(ctx, makeBooking(), time.UTC); !errors.Is(err, ErrBookingStateConflict) {
		t.Fatalf("stale preflight reserved: %v", err)
	}
	if err = s.UpdateBookingSettings(ctx, initial.Calendars, initial.Keywords, "stale overwrite", initial.Calendars); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale keywords overwrote: %v", err)
	}
	current, err := s.BookingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initial = current
	lock, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1128353611,1112493907)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	var wg sync.WaitGroup
	var reserveErr, saveErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		reserveErr = s.ReserveBooking(ctx, makeBooking(), time.UTC)
		done <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		saveErr = s.UpdateBookingSettings(ctx, current.Calendars, current.Keywords, "concurrent keywords", current.Calendars)
		done <- struct{}{}
	}()
	select {
	case <-done:
		t.Fatal("operation bypassed shared configuration/reservation lock")
	case <-time.After(50 * time.Millisecond):
	}
	if err = lock.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if saveErr != nil || reserveErr != nil && !errors.Is(reserveErr, ErrBookingStateConflict) {
		t.Fatalf("save=%v reserve=%v", saveErr, reserveErr)
	}
}
