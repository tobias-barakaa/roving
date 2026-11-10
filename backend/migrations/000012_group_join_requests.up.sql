CREATE TABLE group_join_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id     uuid NOT NULL REFERENCES study_groups(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message      text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'pending',
    decided_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT group_join_requests_message_len
        CHECK (char_length(message) <= 1000),

    CONSTRAINT group_join_requests_status
        CHECK (status IN ('pending', 'approved', 'rejected', 'withdrawn')),

    CONSTRAINT group_join_requests_decided_consistent
        CHECK (
            (status IN ('approved', 'rejected', 'withdrawn'))
            = (decided_at IS NOT NULL)
        )
);

-- Only one pending request per user per group.
CREATE UNIQUE INDEX group_join_requests_group_user_pending_key
    ON group_join_requests (group_id, user_id)
    WHERE status = 'pending';

CREATE INDEX group_join_requests_group_pending_idx
    ON group_join_requests (group_id, created_at DESC)
    WHERE status = 'pending';

CREATE INDEX group_join_requests_user_idx
    ON group_join_requests (user_id, created_at DESC);

CREATE TRIGGER group_join_requests_set_updated_at
    BEFORE UPDATE ON group_join_requests
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- CREATE TABLE group_join_requests (
--     id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
--     group_id     uuid NOT NULL REFERENCES study_groups(id) ON DELETE CASCADE,
--     user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
--     message      text NOT NULL DEFAULT '',
--     status       text NOT NULL DEFAULT 'pending',  -- pending | approved | rejected | withdrawn
--     decided_by   uuid REFERENCES users(id) ON DELETE SET NULL,
--     decided_at   timestamptz,
--     created_at   timestamptz NOT NULL DEFAULT now(),
--     updated_at   timestamptz NOT NULL DEFAULT now(),
    

--         CREATE UNIQUE INDEX group_join_requests_group_user_pending_key
--             ON group_join_requests (group_id, user_id)
--             WHERE status = 'pending';
--     CONSTRAINT group_join_requests_message_len CHECK (char_length(message) <= 1000),
--     CONSTRAINT group_join_requests_status CHECK (status IN ('pending', 'approved', 'rejected', 'withdrawn')),
--     CONSTRAINT group_join_requests_decided_consistent
--         CHECK ((status IN ('approved','rejected','withdrawn')) = (decided_at IS NOT NULL))
-- );
-- CREATE INDEX group_join_requests_group_pending_idx
--     ON group_join_requests (group_id, created_at DESC) WHERE status = 'pending';
-- CREATE INDEX group_join_requests_user_idx
--     ON group_join_requests (user_id, created_at DESC);
-- CREATE TRIGGER group_join_requests_set_updated_at
--     BEFORE UPDATE ON group_join_requests
--     FOR EACH ROW EXECUTE FUNCTION set_updated_at();