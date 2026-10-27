Rovingr auth: setup and decisions
Install
go get golang.org/x/crypto/argon2 golang.org/x/time/rate
go mod tidy
migrate -path backend/migrations -database "$MIGRATE_URL" up   # golang-migrate
Endpoints (all under /api/v1/auth, JSON, credentials: "include" from Astro)
Method	Path	Notes
POST	/register	email + password (8-128 chars), starts a session
POST	/login	generic error on failure, new session ID every login
POST	/logout	deletes the server-side session, clears cookie
GET	/me	current user
POST	/verify-email	{token}
POST	/resend-verification	needs login
POST	/forgot-password	always 202, never reveals if the email exists
POST	/reset-password	{token, password}, signs out all sessions

Astro pages needed: /verify-email?token=..., /reset-password?token=...

IDs: UUID, not 1, 2, 3
Sequential IDs leak how many users/groups/rooms you have and make /groups/17 -> /groups/18 guessing trivial.
UUIDs can also be created without a round trip and merged across systems later.
Honest caveat: unguessable IDs are not access control. Every group/room query must still check membership.
gen_random_uuid() (v4) is built in. If index locality matters later, PostgreSQL 18 has uuidv7().
Never expose an internal ID you would not want guessed; keep using UUIDs for every table.
Database safety checklist
Two roles: a migrator that owns the tables, and an app role that can only read/write rows.
CREATE ROLE rovingr_migrator LOGIN PASSWORD '...';
CREATE ROLE rovingr_app      LOGIN PASSWORD '...';
CREATE DATABASE rovingr OWNER rovingr_migrator;
\c rovingr
REVOKE ALL ON DATABASE rovingr FROM PUBLIC;
GRANT CONNECT ON DATABASE rovingr TO rovingr_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO rovingr_app;
ALTER DEFAULT PRIVILEGES FOR ROLE rovingr_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO rovingr_app;
Run migrations as rovingr_migrator; the API connects as rovingr_app.
docker-compose: publish Postgres as 127.0.0.1:5432:5432, never 5432:5432.
All queries use $1 placeholders (pgx), never string concatenation.
Passwords: Argon2id. Session and email tokens are stored as SHA-256 hashes, so a DB leak does not give attackers working sessions or reset links.
Constraints in SQL (lowercase email, length, unique) so bad data cannot get in even if a handler has a bug.
In database.Connect, add after ParseConfig:
config.MaxConnLifetime = time.Hour
config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
config.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
if os.Getenv("APP_ENV") == "production" &&
    sslmode != "verify-full" && sslmode != "verify-ca" {
    return nil, fmt.Errorf("DB_SSLMODE must be verify-full or verify-ca in production")
}
sslmode=disable is fine for local Docker only.
Back up, and keep .env out of git (check .gitignore).
Things to know
Resend without a domain: as far as I know, onboarding@resend.dev can only deliver to the email address of your own Resend account. Other people will not receive verification or reset emails until you verify a domain. Please confirm in Resend's docs. Until then keep REQUIRE_VERIFIED_EMAIL=false; with no RESEND_API_KEY the links print in the API console, so you can test the whole flow.
localhost vs 127.0.0.1: browsers treat them as different sites, so a SameSite cookie set by 127.0.0.1:8080 will not be sent from a page on localhost:4321. Call the API as http://localhost:8080 from Astro. In production, put both under one registrable domain (app.rovingr.com + api.rovingr.com).
Account enumeration: register returns 409 if an email exists. That is a small leak most apps accept; the stricter option is "check your email" for every signup, which needs working email first.
Rate limits are in-memory per IP (fine on one instance). Behind a proxy, call router.SetTrustedProxies([...]) with the proxy's address, otherwise every user looks like the proxy.
Not compiled here (no Go toolchain in my sandbox). Run go build ./... and go vet ./... and send me any errors.
Folder structure review

Good bones. Changes I'd make:

internal/ appears twice. Merge into one; database/ lives inside it.
Delete backend/internal/database/queries/ unless you adopt sqlc. With pgx, SQL lives in each package's store (users/store.go).
Put *_test.go next to the code, not in backend/tests/. Go convention gives access to unexported functions. Keep tests/ only for cross-package integration tests.
Per package, use the same files: handler.go (HTTP), store.go (SQL), plus a service.go once logic grows. Packages import stores, never each other's handlers.
Add internal/config, internal/httpx (middleware) and internal/mailer (shown here). Add cmd/migrate later if you want migrations run from Go.
database/seeds at repo root is fine, but backend/migrations plus a backend/seeds keeps everything the backend needs in one tree.
Every migration needs a matching .down.sql.