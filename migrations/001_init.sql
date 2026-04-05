CREATE TABLE IF NOT EXISTS meeting_types (
    id BIGSERIAL PRIMARY KEY,
    slug TEXT UNIQUE NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    duration_min INT NOT NULL DEFAULT 30,
    buffer_min INT NOT NULL DEFAULT 10,
    max_per_day INT NOT NULL DEFAULT 8,
    calendar_id TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS working_hours (
    id BIGSERIAL PRIMARY KEY,
    day_of_week INT NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    start_time TEXT NOT NULL DEFAULT '09:00',
    end_time TEXT NOT NULL DEFAULT '17:00',
    active BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (day_of_week)
);

CREATE TABLE IF NOT EXISTS bookings (
    id BIGSERIAL PRIMARY KEY,
    meeting_type_id BIGINT NOT NULL REFERENCES meeting_types(id),
    guest_name TEXT NOT NULL,
    guest_email TEXT NOT NULL,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    status TEXT NOT NULL DEFAULT 'confirmed',
    calendar_event_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bookings_time ON bookings (start_time, end_time);
CREATE INDEX IF NOT EXISTS idx_bookings_status ON bookings (status);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- Default working hours: Mon-Fri 09:00-17:00
INSERT INTO working_hours (day_of_week, start_time, end_time, active) VALUES
    (1, '09:00', '17:00', true),
    (2, '09:00', '17:00', true),
    (3, '09:00', '17:00', true),
    (4, '09:00', '17:00', true),
    (5, '09:00', '17:00', true)
ON CONFLICT (day_of_week) DO NOTHING;
