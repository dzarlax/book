-- Existing bookings remain NULL until their original calendar is verified.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS calendar_id_snapshot TEXT;
