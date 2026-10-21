-- Shared helpers used by the study-platform tables.

-- Keeps updated_at honest even when a query forgets to set it.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

-- Rejects time-zone names PostgreSQL does not know (e.g. "Africa/Nairobi" is
-- valid, "Nairobi" is not). Used by CHECK constraints, which cannot contain
-- sub-queries directly.
CREATE OR REPLACE FUNCTION is_valid_timezone(tz text) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = tz)
$$;