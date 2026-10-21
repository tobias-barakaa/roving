-- A series is the recurring PLAN; study_sessions rows are the actual
-- OCCURRENCES. Attendance and focus data attach to occurrences, so cancelling
-- Tuesday never touches Monday's history.
--
-- Changes to one occurrence (cancel, move) are made on its own study_sessions
-- row (status / starts_at), while original_starts_at keeps the generator's
-- idempotency key. That is why there is no separate exceptions table.
--
-- History is preserved: groups/rooms with sessions cannot be hard-deleted
-- (ON DELETE NO ACTION). Archive them instead.

CREATE TABLE study_session_series (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id         uuid NOT NULL REFERENCES study_groups(id),
    room_id          uuid,
    created_by       uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title            text NOT NULL,
    description      text NOT NULL DEFAULT '',
    timezone         text NOT NULL,                 -- IANA name, e.g. Africa/Nairobi
    recurrence_rule  text NOT NULL,                 -- RFC 5545 RRULE value, e.g. FREQ=WEEKLY;BYDAY=MO,TU
    local_start_time time NOT NULL,                 -- wall-clock time in `timezone`
    duration_seconds integer NOT NULL,
    starts_on        date NOT NULL,
    ends_on          date,
    status           text NOT NULL DEFAULT 'active',
    generated_until  timestamptz,                   -- rolling generation window high-water mark
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT study_session_series_id_group_key UNIQUE (id, group_id),
    CONSTRAINT study_session_series_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id),
    CONSTRAINT study_session_series_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 150),
    CONSTRAINT study_session_series_description_len CHECK (char_length(description) <= 2000),
    CONSTRAINT study_session_series_timezone CHECK (is_valid_timezone(timezone)),
    CONSTRAINT study_session_series_rrule CHECK (
        char_length(recurrence_rule) <= 500
        AND recurrence_rule ~ '^FREQ=(DAILY|WEEKLY|MONTHLY)(;[A-Z]+=[A-Z0-9,+-]+)*$'),
    CONSTRAINT study_session_series_duration CHECK (duration_seconds BETWEEN 60 AND 86400),
    CONSTRAINT study_session_series_date_range CHECK (ends_on IS NULL OR ends_on >= starts_on),
    CONSTRAINT study_session_series_status CHECK (status IN ('active', 'paused', 'ended'))
);
CREATE INDEX study_session_series_group_idx ON study_session_series (group_id, status);
CREATE INDEX study_session_series_room_idx  ON study_session_series (room_id) WHERE room_id IS NOT NULL;
-- Background generator: "active series whose window needs extending".
CREATE INDEX study_session_series_generation_idx ON study_session_series (generated_until) WHERE status = 'active';
CREATE TRIGGER study_session_series_set_updated_at
    BEFORE UPDATE ON study_session_series
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE study_sessions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id           uuid NOT NULL REFERENCES study_groups(id),
    room_id            uuid,
    series_id          uuid,
    original_starts_at timestamptz,                 -- generator key; NULL for one-off sessions
    created_by         uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title              text NOT NULL,
    description        text NOT NULL DEFAULT '',
    starts_at          timestamptz NOT NULL,
    ends_at            timestamptz NOT NULL,
    status             text NOT NULL DEFAULT 'scheduled',
    cancelled_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT study_sessions_id_group_key UNIQUE (id, group_id),
    -- A room can only be used by sessions of its own group.
    CONSTRAINT study_sessions_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id),
    CONSTRAINT study_sessions_series_fk
        FOREIGN KEY (series_id, group_id) REFERENCES study_session_series (id, group_id),
    -- Re-running the generator can never create the same occurrence twice.
    CONSTRAINT study_sessions_series_occurrence_key UNIQUE (series_id, original_starts_at),
    CONSTRAINT study_sessions_series_key_pair CHECK ((series_id IS NULL) = (original_starts_at IS NULL)),
    CONSTRAINT study_sessions_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 150),
    CONSTRAINT study_sessions_description_len CHECK (char_length(description) <= 2000),
    CONSTRAINT study_sessions_time_order CHECK (ends_at > starts_at),
    CONSTRAINT study_sessions_max_length CHECK (ends_at - starts_at <= interval '24 hours'),
    CONSTRAINT study_sessions_status CHECK (status IN ('scheduled', 'completed', 'cancelled')),
    CONSTRAINT study_sessions_cancel_consistent CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);
CREATE INDEX study_sessions_group_starts_idx ON study_sessions (group_id, starts_at);
CREATE INDEX study_sessions_room_starts_idx  ON study_sessions (room_id, starts_at) WHERE room_id IS NOT NULL;
-- Reminders / "mark finished" jobs scan upcoming scheduled sessions.
CREATE INDEX study_sessions_upcoming_idx ON study_sessions (starts_at) WHERE status = 'scheduled';
CREATE TRIGGER study_sessions_set_updated_at
    BEFORE UPDATE ON study_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();