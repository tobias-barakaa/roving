-- Personal Pomodoro tracking. Context (room / scheduled session) is optional
-- and cleared, not cascaded, if that context disappears: a user's focus
-- history should outlive a room. The service must verify the user may
-- participate in any context ID it accepts, and that a cycle and its
-- intervals agree on that context.

CREATE TABLE focus_cycles (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    study_session_id     uuid REFERENCES study_sessions(id) ON DELETE SET NULL,
    room_id              uuid REFERENCES study_rooms(id) ON DELETE SET NULL,
    status               text NOT NULL DEFAULT 'planned',
    planned_focus_seconds integer NOT NULL DEFAULT 1500,   -- 25 min
    planned_break_seconds integer NOT NULL DEFAULT 300,    -- 5 min
    planned_rounds       smallint NOT NULL DEFAULT 4,
    started_at           timestamptz,
    ended_at             timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT focus_cycles_status CHECK (status IN ('planned', 'running', 'completed', 'cancelled')),
    CONSTRAINT focus_cycles_focus_seconds CHECK (planned_focus_seconds BETWEEN 60 AND 14400),
    CONSTRAINT focus_cycles_break_seconds CHECK (planned_break_seconds BETWEEN 0 AND 3600),
    CONSTRAINT focus_cycles_rounds CHECK (planned_rounds BETWEEN 1 AND 20),
    CONSTRAINT focus_cycles_time_order CHECK (ended_at IS NULL OR (started_at IS NOT NULL AND ended_at >= started_at))
);
CREATE INDEX focus_cycles_user_idx ON focus_cycles (user_id, created_at DESC);
CREATE INDEX focus_cycles_session_idx ON focus_cycles (study_session_id) WHERE study_session_id IS NOT NULL;
CREATE INDEX focus_cycles_room_idx    ON focus_cycles (room_id) WHERE room_id IS NOT NULL;
-- A person can only have one cycle running at a time.
CREATE UNIQUE INDEX focus_cycles_one_running_idx ON focus_cycles (user_id) WHERE status = 'running';
CREATE TRIGGER focus_cycles_set_updated_at
    BEFORE UPDATE ON focus_cycles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE focus_sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cycle_id         uuid REFERENCES focus_cycles(id) ON DELETE CASCADE,
    study_session_id uuid REFERENCES study_sessions(id) ON DELETE SET NULL,
    room_id          uuid REFERENCES study_rooms(id) ON DELETE SET NULL,
    sequence_no      smallint,                         -- order inside the cycle
    kind             text NOT NULL DEFAULT 'focus',
    status           text NOT NULL DEFAULT 'planned',
    planned_seconds  integer NOT NULL,
    started_at       timestamptz,
    ended_at         timestamptz,
    -- Derived from server timestamps; clients can never report their own duration.
    actual_seconds   integer GENERATED ALWAYS AS (
        CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL
             THEN EXTRACT(EPOCH FROM (ended_at - started_at))::integer
        END) STORED,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT focus_sessions_cycle_sequence_key UNIQUE (cycle_id, sequence_no),
    CONSTRAINT focus_sessions_kind   CHECK (kind IN ('focus', 'short_break', 'long_break')),
    CONSTRAINT focus_sessions_status CHECK (status IN ('planned', 'running', 'completed', 'cancelled', 'interrupted')),
    CONSTRAINT focus_sessions_planned_seconds CHECK (planned_seconds BETWEEN 1 AND 14400),
    CONSTRAINT focus_sessions_time_order CHECK (ended_at IS NULL OR started_at IS NULL OR ended_at >= started_at),
    -- Only legal state/timestamp combinations can be stored.
    CONSTRAINT focus_sessions_state_consistent CHECK (
           (status = 'planned'   AND started_at IS NULL     AND ended_at IS NULL)
        OR (status = 'running'   AND started_at IS NOT NULL AND ended_at IS NULL)
        OR (status IN ('completed', 'interrupted') AND started_at IS NOT NULL AND ended_at IS NOT NULL)
        OR (status = 'cancelled' AND ended_at IS NOT NULL)
    )
);
CREATE INDEX focus_sessions_user_started_idx ON focus_sessions (user_id, started_at DESC);
CREATE INDEX focus_sessions_session_idx ON focus_sessions (study_session_id) WHERE study_session_id IS NOT NULL;
CREATE INDEX focus_sessions_room_idx    ON focus_sessions (room_id) WHERE room_id IS NOT NULL;
-- One running interval per user, enforced by PostgreSQL rather than hoped for in Go.
CREATE UNIQUE INDEX focus_sessions_one_running_idx ON focus_sessions (user_id) WHERE status = 'running';
CREATE TRIGGER focus_sessions_set_updated_at
    BEFORE UPDATE ON focus_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();