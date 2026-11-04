-- In-app notifications. One row per recipient. Rows are disposable (read ones
-- are purged by the worker), so they are not history.
--
-- dedupe_key makes fan-out idempotent: the same logical event (e.g. a reminder
-- for one session start time) can be inserted any number of times and each user
-- still gets exactly one row. NULL keys never collide.

CREATE TABLE notifications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id   uuid REFERENCES study_groups(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    title      text NOT NULL,
    body       text NOT NULL DEFAULT '',
    data       jsonb NOT NULL DEFAULT '{}'::jsonb,
    dedupe_key text,
    read_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notifications_user_dedupe_key UNIQUE (user_id, dedupe_key),
    CONSTRAINT notifications_kind_format  CHECK (kind ~ '^[a-z][a-z_]{0,49}$'),
    CONSTRAINT notifications_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT notifications_body_length  CHECK (char_length(body) <= 2000),
    CONSTRAINT notifications_data_object  CHECK (jsonb_typeof(data) = 'object'),
    CONSTRAINT notifications_dedupe_length CHECK (dedupe_key IS NULL OR char_length(dedupe_key) <= 200)
);
-- Inbox listing.
CREATE INDEX notifications_user_created_idx ON notifications (user_id, created_at DESC, id DESC);
-- Unread badge count.
CREATE INDEX notifications_user_unread_idx ON notifications (user_id) WHERE read_at IS NULL;
-- Retention job.
CREATE INDEX notifications_read_created_idx ON notifications (created_at) WHERE read_at IS NOT NULL;

-- Opt-out model: no row means "enabled". A row with in_app = false mutes a kind.
CREATE TABLE notification_preferences (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    in_app     boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind),
    CONSTRAINT notification_preferences_kind_format CHECK (kind ~ '^[a-z][a-z_]{0,49}$')
);
CREATE TRIGGER notification_preferences_set_updated_at
    BEFORE UPDATE ON notification_preferences
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();