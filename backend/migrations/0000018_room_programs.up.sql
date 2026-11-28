CREATE TABLE room_programs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    room_id UUID NOT NULL REFERENCES study_rooms(id) ON DELETE CASCADE,

    total_rounds INTEGER NOT NULL,
    focus_seconds INTEGER NOT NULL,
    break_seconds INTEGER NOT NULL,

    current_round INTEGER NOT NULL DEFAULT 0,
    current_phase TEXT NOT NULL DEFAULT 'focus',

    status TEXT NOT NULL DEFAULT 'draft',

    phase_started_at TIMESTAMPTZ,
    phase_ends_at TIMESTAMPTZ,

    started_by UUID REFERENCES users(id),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (total_rounds BETWEEN 1 AND 20),
    CHECK (focus_seconds BETWEEN 60 AND 14400),
    CHECK (break_seconds BETWEEN 0 AND 3600),
    CHECK (current_round BETWEEN 0 AND total_rounds),
    CHECK (current_phase IN ('focus', 'break')),
    CHECK (status IN ('draft', 'running', 'completed', 'cancelled'))
);


-- migrate -path migrations -database "postgres://admin_user:S3cr3tP@ssw0rd@://amazonaws.com"
-- migrate -path migrations -database "postgres://$DB_USER:$DB_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=$DB_SSLMODE" up