-- Rooms belong to exactly one group. Live presence (who is online right now)
-- is NOT stored here; that belongs to the WebSocket layer.

CREATE TABLE study_rooms (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    uuid NOT NULL REFERENCES study_groups(id) ON DELETE CASCADE,
    created_by  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    -- 'group'      : every active group member may enter
    -- 'restricted' : only users with an active room_memberships row
    access      text NOT NULL DEFAULT 'group',
    capacity    integer,
    settings    jsonb NOT NULL DEFAULT '{}'::jsonb,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- Target for composite foreign keys that must prove "this room belongs to this group".
    CONSTRAINT study_rooms_id_group_key UNIQUE (id, group_id),
    CONSTRAINT study_rooms_name_length  CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT study_rooms_description_len CHECK (char_length(description) <= 2000),
    CONSTRAINT study_rooms_access       CHECK (access IN ('group', 'restricted')),
    CONSTRAINT study_rooms_capacity     CHECK (capacity IS NULL OR capacity BETWEEN 1 AND 1000),
    CONSTRAINT study_rooms_settings_object CHECK (jsonb_typeof(settings) = 'object')
);
CREATE INDEX study_rooms_group_idx ON study_rooms (group_id) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX study_rooms_group_name_key ON study_rooms (group_id, lower(name)) WHERE archived_at IS NULL;
CREATE TRIGGER study_rooms_set_updated_at
    BEFORE UPDATE ON study_rooms
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Explicit access list / room roles. group_id is stored redundantly so the
-- database itself proves (a) the room belongs to that group and (b) the user
-- has a membership row in that group.
CREATE TABLE room_memberships (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id    uuid NOT NULL,
    group_id   uuid NOT NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'member',
    status     text NOT NULL DEFAULT 'active',
    joined_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT room_memberships_room_user_key UNIQUE (room_id, user_id),
    CONSTRAINT room_memberships_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id) ON DELETE CASCADE,
    CONSTRAINT room_memberships_group_member_fk
        FOREIGN KEY (group_id, user_id) REFERENCES group_memberships (group_id, user_id) ON DELETE CASCADE,
    CONSTRAINT room_memberships_role   CHECK (role IN ('member', 'host')),
    CONSTRAINT room_memberships_status CHECK (status IN ('active', 'revoked')),
    CONSTRAINT room_memberships_revoked_consistent CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);
CREATE INDEX room_memberships_user_status_idx ON room_memberships (user_id, status);
CREATE INDEX room_memberships_group_idx ON room_memberships (group_id);
CREATE TRIGGER room_memberships_set_updated_at
    BEFORE UPDATE ON room_memberships
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();