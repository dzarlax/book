CREATE TABLE IF NOT EXISTS meeting_type_hours (
    id BIGSERIAL PRIMARY KEY,
    meeting_type_id BIGINT NOT NULL REFERENCES meeting_types(id) ON DELETE CASCADE,
    day_of_week INT NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    start_time TEXT NOT NULL DEFAULT '09:00',
    end_time TEXT NOT NULL DEFAULT '17:00',
    active BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (meeting_type_id, day_of_week)
);
