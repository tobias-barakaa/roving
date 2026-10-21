-- Groups: the shared community. Ownership is derived from the membership row
-- with role = 'owner' (single source of truth, no separate owner_id to drift).

CREATE TABLE study_groups (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_by  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name        text NOT NULL,
    slug        text NOT NULL,
    description text NOT NULL DEFAULT '',
    visibility  text NOT NULL DEFAULT 'private',
    settings    jsonb NOT NULL DEFAULT '{}'::jsonb,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT study_groups_slug_key        UNIQUE (slug),
    CONSTRAINT study_groups_name_length     CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT study_groups_slug_format     CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND char_length(slug) BETWEEN 3 AND 60),
    CONSTRAINT study_groups_description_len CHECK (char_length(description) <= 2000),
    CONSTRAINT study_groups_visibility      CHECK (visibility IN ('private', 'public')),
    CONSTRAINT study_groups_settings_object CHECK (jsonb_typeof(settings) = 'object')
);
CREATE INDEX study_groups_created_by_idx ON study_groups (created_by);
CREATE INDEX study_groups_public_idx
    ON study_groups (created_at DESC)
    WHERE visibility = 'public' AND archived_at IS NULL;
CREATE TRIGGER study_groups_set_updated_at
    BEFORE UPDATE ON study_groups
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE group_memberships (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   uuid NOT NULL REFERENCES study_groups(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'member',
    status     text NOT NULL DEFAULT 'active',
    invited_by uuid REFERENCES users(id) ON DELETE SET NULL,
    joined_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT group_memberships_group_user_key UNIQUE (group_id, user_id),
    CONSTRAINT group_memberships_role   CHECK (role IN ('owner', 'admin', 'moderator', 'member')),
    CONSTRAINT group_memberships_status CHECK (status IN ('pending', 'active', 'suspended', 'left')),
    CONSTRAINT group_memberships_owner_is_active CHECK (role <> 'owner' OR status = 'active'),
    CONSTRAINT group_memberships_active_has_joined_at CHECK (status <> 'active' OR joined_at IS NOT NULL)
);
-- Listing "my groups".
CREATE INDEX group_memberships_user_status_idx ON group_memberships (user_id, status);
-- Authorization checks and member lists: "active admins of group X".
CREATE INDEX group_memberships_group_status_role_idx ON group_memberships (group_id, status, role);
-- At most one owner per group. (Ownership transfer = demote, then promote, in one transaction.)
CREATE UNIQUE INDEX group_memberships_one_owner_idx ON group_memberships (group_id) WHERE role = 'owner';
CREATE TRIGGER group_memberships_set_updated_at
    BEFORE UPDATE ON group_memberships
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A live group must always have an active owner. The checks are deferred to
-- COMMIT so creating a group (group row + owner membership) and transferring
-- ownership can happen inside one transaction.
CREATE FUNCTION assert_group_has_owner(p_group_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM study_groups WHERE id = p_group_id AND archived_at IS NULL)
       AND NOT EXISTS (
            SELECT 1 FROM group_memberships
            WHERE group_id = p_group_id AND role = 'owner' AND status = 'active')
    THEN
        RAISE EXCEPTION 'group % must have an active owner', p_group_id
            USING ERRCODE = 'check_violation';
    END IF;
END;
$$;

CREATE FUNCTION group_memberships_owner_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_group_has_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.group_id ELSE NEW.group_id END);
    RETURN NULL;
END;
$$;

CREATE FUNCTION study_groups_owner_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM assert_group_has_owner(NEW.id);
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER group_memberships_require_owner
    AFTER UPDATE OR DELETE ON group_memberships
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION group_memberships_owner_guard();

CREATE CONSTRAINT TRIGGER study_groups_require_owner
    AFTER INSERT ON study_groups
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION study_groups_owner_guard();