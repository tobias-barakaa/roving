-- Audio/video calls inside study rooms.
--
-- A call is one live meeting in a room; at most ONE is active per room
-- (partial unique index). call_participants keeps one row per JOIN, so
-- history survives rejoins, and a user can be in at most one call at a time.
-- Only signaling/state lives here; the media itself is peer-to-peer WebRTC.

CREATE TABLE room_calls (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   uuid NOT NULL,
    room_id    uuid NOT NULL,
    started_by uuid REFERENCES users(id) ON DELETE SET NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,
    -- A room's call can only belong to the room's own group.
    CONSTRAINT room_calls_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id),
    CONSTRAINT room_calls_time_order CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE UNIQUE INDEX room_calls_one_active_idx ON room_calls (room_id) WHERE ended_at IS NULL;
CREATE INDEX room_calls_room_started_idx ON room_calls (room_id, started_at DESC);

CREATE TABLE call_participants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id    uuid NOT NULL REFERENCES room_calls(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    audio_on   boolean NOT NULL DEFAULT true,
    video_on   boolean NOT NULL DEFAULT false,
    joined_at  timestamptz NOT NULL DEFAULT now(),
    left_at    timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT call_participants_time_order CHECK (left_at IS NULL OR left_at >= joined_at)
);
-- One live call per person, enforced by PostgreSQL (also the ON CONFLICT target).
CREATE UNIQUE INDEX call_participants_one_active_per_user_idx
    ON call_participants (user_id) WHERE left_at IS NULL;
CREATE INDEX call_participants_active_idx ON call_participants (call_id) WHERE left_at IS NULL;
CREATE TRIGGER call_participants_set_updated_at
    BEFORE UPDATE ON call_participants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---- down ----
-- DROP TABLE IF EXISTS call_participants;
-- DROP TABLE IF EXISTS room_calls;