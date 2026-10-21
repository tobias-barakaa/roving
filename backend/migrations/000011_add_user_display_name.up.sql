-- Late correction: users.Create inserts into display_name, but migration 1
-- (already applied) did not create the column. Idempotent for the development
-- database (where the column, indexes, and trigger already exist out-of-band)
-- and correct for a fresh database. Fails loudly rather than silently
-- accepting unsafe data.

-- 1. Column (idempotent). This only skips if the column already exists; it does
--    not verify a pre-existing column's type — step 2 does that.
ALTER TABLE public.users ADD COLUMN IF NOT EXISTS display_name text;

-- 2. Early guard, BEFORE any backfill/dedupe write. Verify the column type and
--    any pre-existing indexes against the expected schema; fail if anything is
--    inconsistent rather than assuming a name match is sufficient.
DO $$
DECLARE
    ok    boolean;
    nulls bigint;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'users'
          AND column_name = 'display_name'
          AND data_type <> 'text'
    ) THEN
        RAISE EXCEPTION 'public.users.display_name exists but has an unexpected data type; expected text';
    END IF;

    IF to_regclass('public.users_display_name_ci_idx') IS NOT NULL THEN
        SELECT (i.indexrelid IS NOT NULL)
               AND i.indrelid = 'public.users'::regclass
               AND i.indisunique
               AND i.indisvalid
               AND i.indisready
               AND i.indnkeyatts = 1
               AND i.indnatts = 1
               AND i.indpred IS NULL
               AND replace(pg_get_expr(i.indexprs, i.indrelid), ' ', '') = 'lower(display_name)'
        INTO ok
        FROM pg_class c
        LEFT JOIN pg_index i ON i.indexrelid = c.oid
        WHERE c.oid = 'public.users_display_name_ci_idx'::regclass;

        IF ok IS DISTINCT FROM true THEN
            RAISE EXCEPTION
                'public.users_display_name_ci_idx is not a valid, ready, unique, non-partial, single-column expression index on public.users(lower(display_name))';
        END IF;
    END IF;

    IF to_regclass('public.users_email_ci_idx') IS NOT NULL THEN
        SELECT (i.indexrelid IS NOT NULL)
               AND i.indrelid = 'public.users'::regclass
               AND i.indisunique
               AND i.indisvalid
               AND i.indisready
               AND i.indnkeyatts = 1
               AND i.indnatts = 1
               AND i.indpred IS NULL
               AND replace(pg_get_expr(i.indexprs, i.indrelid), ' ', '') = 'lower(email)'
        INTO ok
        FROM pg_class c
        LEFT JOIN pg_index i ON i.indexrelid = c.oid
        WHERE c.oid = 'public.users_email_ci_idx'::regclass;

        IF ok IS DISTINCT FROM true THEN
            RAISE EXCEPTION
                'public.users_email_ci_idx is not a valid, ready, unique, non-partial, single-column expression index on public.users(lower(email))';
        END IF;
    END IF;

    SELECT count(*) INTO nulls
    FROM public.users
    WHERE display_name IS NULL OR btrim(display_name) = '';

    IF to_regclass('public.users_display_name_ci_idx') IS NOT NULL AND nulls > 0 THEN
        RAISE EXCEPTION
            'public.users_display_name_ci_idx already exists, but % users have a null/empty display_name. '
            'Backfilling would risk violating the unique index; set each to a unique name manually, '
            'then re-run this migration.', nulls;
    END IF;
END $$;

-- 3. Backfill null/empty names (safe: either no index, or no null/empty rows).
UPDATE public.users
SET display_name = lower(split_part(email, '@', 1))
WHERE display_name IS NULL OR btrim(display_name) = '';

-- 4. De-duplicate case-insensitive collisions (full-UUID suffix is globally
--    unique among generated names).
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY lower(display_name)
               ORDER BY created_at, id
           ) AS rn
    FROM public.users
    WHERE display_name IS NOT NULL AND btrim(display_name) <> ''
)
UPDATE public.users u
SET display_name = u.display_name || '-' || u.id::text
FROM ranked r
WHERE u.id = r.id AND r.rn > 1;

-- 5. Enforce NOT NULL.
ALTER TABLE public.users ALTER COLUMN display_name SET NOT NULL;

-- 6. Data safety: refuse to continue unless every name is non-null, non-empty,
--    and unique under lower(display_name).
DO $$
DECLARE
    nulls     bigint;
    dup_names text;
BEGIN
    SELECT count(*) INTO nulls
    FROM public.users
    WHERE display_name IS NULL OR btrim(display_name) = '';

    SELECT string_agg(ln, ', ') INTO dup_names
    FROM (
        SELECT lower(display_name) AS ln
        FROM public.users
        GROUP BY lower(display_name)
        HAVING count(*) > 1
    ) d;

    IF nulls > 0 OR dup_names IS NOT NULL THEN
        RAISE EXCEPTION
            'display_name cannot be made unique automatically: % null/empty value(s), '
            'duplicate case-insensitive name(s): [%]. '
            'Manually rename the conflicting users so lower(display_name) is unique, '
            'then re-run this migration.',
            nulls, coalesce(dup_names, 'none');
    END IF;
END $$;

-- 7. Create (or skip the now-verified) unique indexes.
CREATE UNIQUE INDEX IF NOT EXISTS users_display_name_ci_idx
    ON public.users (lower(display_name));

CREATE UNIQUE INDEX IF NOT EXISTS users_email_ci_idx
    ON public.users (lower(email));

-- 8. Re-assert the canonical trigger. On the development database this trigger
--    pre-existed out-of-band; DROP+CREATE adopts it into this migration.
DROP TRIGGER IF EXISTS users_set_updated_at ON public.users;
CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON public.users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
