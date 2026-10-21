DROP TABLE IF EXISTS group_memberships;
DROP TABLE IF EXISTS study_groups;
DROP FUNCTION IF EXISTS study_groups_owner_guard();
DROP FUNCTION IF EXISTS group_memberships_owner_guard();
DROP FUNCTION IF EXISTS assert_group_has_owner(uuid);