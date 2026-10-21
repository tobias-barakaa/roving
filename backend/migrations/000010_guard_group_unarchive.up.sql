-- A live group must always have an active owner. Existing deferred triggers
-- cover insert and membership changes; this adds the missing unarchive
-- transition: reactivating a group (archived_at back to NULL) must not produce
-- an ownerless active group. Reuses assert_group_has_owner (000005), which
-- only enforces for groups with archived_at IS NULL.

CREATE FUNCTION study_groups_unarchive_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.archived_at IS NOT NULL AND NEW.archived_at IS NULL THEN
        PERFORM assert_group_has_owner(NEW.id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER study_groups_require_owner_on_unarchive
    AFTER UPDATE OF archived_at ON study_groups
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION study_groups_unarchive_guard();
