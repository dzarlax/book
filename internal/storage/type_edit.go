package storage

import (
	"context"
	"github.com/dzarlax/book/internal/model"
)

// UpdateMeetingTypeAtCalendar prevents a stale form from restoring a calendar
// that was changed while the form or its capability check was in flight.
func (s *Storage) UpdateMeetingTypeAtCalendar(ctx context.Context, mt *model.MeetingType, expected string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE meeting_types SET slug=$1,title=$2,description=$3,duration_min=$4,buffer_min=$5,max_per_day=$6,calendar_id=$7,video_call=$8,active=$9 WHERE id=$10 AND calendar_id=$11`, mt.Slug, mt.Title, mt.Description, mt.DurationMin, mt.BufferMin, mt.MaxPerDay, mt.CalendarID, mt.VideoCall, mt.Active, mt.ID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSettingsConflict
	}
	return nil
}
