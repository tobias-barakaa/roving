-- Chat. A message belongs to a group and optionally to one of its rooms:
--   room_id IS NULL     → the group-wide channel
--   room_id IS NOT NULL → that room's channel
--
-- Messages are soft-deleted (body blanked, tombstone kept) so reply chains and
-- history positions stay coherent. If a user deletes their account their
-- messages remain, anonymised (user_id becomes NULL).
--
-- The service guarantees a reply targets a message in the SAME channel; the
-- composite FK below guarantees it is at least in the same group.

CREATE TABLE chat_messages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    uuid NOT NULL REFERENCES study_groups(id),
    room_id     uuid,
    user_id     uuid REFERENCES users(id) ON DELETE SET NULL,
    reply_to_id uuid,
    body        text NOT NULL DEFAULT '',
    edited_at   timestamptz,
    deleted_at  timestamptz,
    deleted_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_messages_id_group_key UNIQUE (id, group_id),
    -- A room's chat can only carry messages of the room's own group.
    CONSTRAINT chat_messages_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id),
    CONSTRAINT chat_messages_reply_fk
        FOREIGN KEY (reply_to_id, group_id) REFERENCES chat_messages (id, group_id),
    CONSTRAINT chat_messages_body_length CHECK (
        deleted_at IS NOT NULL OR char_length(btrim(body)) BETWEEN 1 AND 4000),
    CONSTRAINT chat_messages_deleted_blank CHECK (deleted_at IS NULL OR body = ''),
    CONSTRAINT chat_messages_deleted_by_scope CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
-- History pages: newest first, per channel.
CREATE INDEX chat_messages_group_idx
    ON chat_messages (group_id, created_at DESC, id DESC) WHERE room_id IS NULL;
CREATE INDEX chat_messages_room_idx
    ON chat_messages (room_id, created_at DESC, id DESC) WHERE room_id IS NOT NULL;

-- Per-user "read up to here" marker per channel, for unread counts.
-- No row means "read since joining the group".
CREATE TABLE chat_read_state (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id     uuid NOT NULL,
    room_id      uuid,
    last_read_at timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_read_state_member_fk
        FOREIGN KEY (group_id, user_id) REFERENCES group_memberships (group_id, user_id) ON DELETE CASCADE,
    CONSTRAINT chat_read_state_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id) ON DELETE CASCADE
);
-- NULL room_id would defeat a plain UNIQUE, so one partial index per channel kind.
CREATE UNIQUE INDEX chat_read_state_group_key
    ON chat_read_state (user_id, group_id) WHERE room_id IS NULL;
CREATE UNIQUE INDEX chat_read_state_room_key
    ON chat_read_state (user_id, room_id) WHERE room_id IS NOT NULL;
CREATE TRIGGER chat_read_state_set_updated_at
    BEFORE UPDATE ON chat_read_state
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();