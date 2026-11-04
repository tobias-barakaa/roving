-- Reading: a group's shared reading list plus each member's personal progress.
-- Materials are archived, never hard-deleted, so progress history survives.
-- Notes: personal by default, optionally attached to a group / material /
-- session, and optionally shared with the group.

CREATE TABLE reading_materials (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    uuid NOT NULL REFERENCES study_groups(id),
    added_by    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title       text NOT NULL,
    kind        text NOT NULL DEFAULT 'other',
    author      text NOT NULL DEFAULT '',
    url         text,
    description text NOT NULL DEFAULT '',
    unit        text NOT NULL DEFAULT 'pages',
    total_units integer,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- Target for composite FKs proving "this material belongs to this group".
    CONSTRAINT reading_materials_id_group_key UNIQUE (id, group_id),
    CONSTRAINT reading_materials_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT reading_materials_kind CHECK (kind IN ('book', 'article', 'paper', 'video', 'link', 'other')),
    CONSTRAINT reading_materials_author_length CHECK (char_length(author) <= 200),
    CONSTRAINT reading_materials_url CHECK (url IS NULL OR (url ~* '^https?://' AND char_length(url) <= 2000)),
    CONSTRAINT reading_materials_description_len CHECK (char_length(description) <= 2000),
    CONSTRAINT reading_materials_unit CHECK (unit IN ('pages', 'chapters', 'minutes', 'sections')),
    CONSTRAINT reading_materials_total_units CHECK (total_units IS NULL OR total_units BETWEEN 1 AND 1000000)
);
CREATE INDEX reading_materials_group_idx ON reading_materials (group_id, created_at DESC) WHERE archived_at IS NULL;
CREATE TRIGGER reading_materials_set_updated_at
    BEFORE UPDATE ON reading_materials
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE reading_progress (
    material_id  uuid NOT NULL,
    group_id     uuid NOT NULL,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    current_unit integer NOT NULL DEFAULT 0,
    status       text NOT NULL DEFAULT 'not_started',
    started_at   timestamptz,
    finished_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (material_id, user_id),
    CONSTRAINT reading_progress_material_fk
        FOREIGN KEY (material_id, group_id) REFERENCES reading_materials (id, group_id) ON DELETE CASCADE,
    -- The database itself proves the reader is a member of the material's group.
    CONSTRAINT reading_progress_member_fk
        FOREIGN KEY (group_id, user_id) REFERENCES group_memberships (group_id, user_id) ON DELETE CASCADE,
    CONSTRAINT reading_progress_unit CHECK (current_unit BETWEEN 0 AND 1000000),
    CONSTRAINT reading_progress_status CHECK (status IN ('not_started', 'reading', 'finished')),
    CONSTRAINT reading_progress_state_consistent CHECK (
           (status = 'not_started' AND started_at IS NULL     AND finished_at IS NULL)
        OR (status = 'reading'     AND started_at IS NOT NULL AND finished_at IS NULL)
        OR (status = 'finished'    AND started_at IS NOT NULL AND finished_at IS NOT NULL)
    )
);
CREATE INDEX reading_progress_user_idx ON reading_progress (user_id, updated_at DESC);
CREATE TRIGGER reading_progress_set_updated_at
    BEFORE UPDATE ON reading_progress
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE notes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id         uuid REFERENCES study_groups(id),
    material_id      uuid,
    study_session_id uuid,
    title            text NOT NULL DEFAULT '',
    body             text NOT NULL DEFAULT '',
    visibility       text NOT NULL DEFAULT 'private',
    pinned           boolean NOT NULL DEFAULT false,
    tags             text[] NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notes_material_fk
        FOREIGN KEY (material_id, group_id) REFERENCES reading_materials (id, group_id),
    CONSTRAINT notes_session_fk
        FOREIGN KEY (study_session_id, group_id) REFERENCES study_sessions (id, group_id),
    CONSTRAINT notes_title_length CHECK (char_length(title) <= 200),
    CONSTRAINT notes_body_length  CHECK (char_length(body) <= 50000),
    CONSTRAINT notes_not_empty    CHECK (btrim(title) <> '' OR btrim(body) <> ''),
    CONSTRAINT notes_visibility   CHECK (visibility IN ('private', 'group')),
    CONSTRAINT notes_group_visible_has_group CHECK (visibility = 'private' OR group_id IS NOT NULL),
    CONSTRAINT notes_context_has_group
        CHECK ((material_id IS NULL AND study_session_id IS NULL) OR group_id IS NOT NULL),
    CONSTRAINT notes_tags_count CHECK (cardinality(tags) <= 10)
);
CREATE INDEX notes_user_updated_idx ON notes (user_id, pinned DESC, updated_at DESC);
CREATE INDEX notes_group_shared_idx ON notes (group_id, updated_at DESC) WHERE visibility = 'group';
CREATE INDEX notes_material_idx ON notes (material_id) WHERE material_id IS NOT NULL;
CREATE INDEX notes_session_idx  ON notes (study_session_id) WHERE study_session_id IS NOT NULL;
CREATE TRIGGER notes_set_updated_at
    BEFORE UPDATE ON notes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();