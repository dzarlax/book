package storage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dzarlax/book/internal/model"
)

var ErrSettingsConflict = errors.New("settings changed; refresh before saving")

// UpdateBookingSettings atomically compares the selection shown by the form
// and saves both settings, preserving a concurrently changed configuration.
func (s *Storage) UpdateBookingSettings(ctx context.Context, expected, expectedKeywords, keywords, calendars string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1128353611,1112493907)`); err != nil {
		return err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='block_calendars' FOR UPDATE`).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var currentKeywords string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='block_allday_keywords' FOR UPDATE`).Scan(&currentKeywords)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != expected || currentKeywords != expectedKeywords {
		return ErrSettingsConflict
	}
	for _, value := range []struct{ key, value string }{{"block_allday_keywords", keywords}, {"block_calendars", calendars}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, value.key, value.value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Storage) BookingSettings(ctx context.Context) (model.BookingSettings, error) {
	return readBookingSettings(ctx, s.db)
}
func readBookingSettings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (model.BookingSettings, error) {
	var settings model.BookingSettings
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM settings WHERE key='block_calendars'),''),COALESCE((SELECT value FROM settings WHERE key='block_allday_keywords'),'')`).Scan(&settings.Calendars, &settings.Keywords)
	return settings, err
}
