-- Lossy by design: DROP COLUMN permanently deletes the display-name data.
-- Development-only; never run against populated production data without first
-- exporting the column.
--
-- Trigger removal: on the development database, users_set_updated_at pre-existed
-- migration 11 out-of-band and was only adopted (DROP+CREATE) by it. Rolling back
-- therefore also removes that pre-existing trigger. Re-create it manually if the
-- updated_at maintenance is still wanted:
--   CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON public.users
--       FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS users_set_updated_at ON public.users;
DROP INDEX IF EXISTS users_email_ci_idx;
DROP INDEX IF EXISTS users_display_name_ci_idx;
ALTER TABLE public.users DROP COLUMN IF EXISTS display_name;
