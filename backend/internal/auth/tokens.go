package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewToken returns a random URL-safe token (given to the user) and its
// SHA-256 hash (stored in the database).
func NewToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ---------- sessions ----------

var ErrInvalidSession = errors.New("invalid session")

type SessionStore struct {
	db      *pgxpool.Pool
	ttl     time.Duration
	idleTTL time.Duration
}

func NewSessionStore(db *pgxpool.Pool, ttl, idleTTL time.Duration) *SessionStore {
	return &SessionStore{db: db, ttl: ttl, idleTTL: idleTTL}
}

func (s *SessionStore) Create(ctx context.Context, userID string) (string, time.Time, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(s.ttl)
	_, err = s.db.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1::uuid, $2, $3)`,
		userID, hash, exp)
	return raw, exp, err
}

// Lookup checks absolute and idle expiry and refreshes last_seen_at.
func (s *SessionStore) Lookup(ctx context.Context, raw string) (string, error) {
	var userID string
	err := s.db.QueryRow(ctx,
		`UPDATE sessions SET last_seen_at = now()
		 WHERE token_hash = $1
		   AND expires_at > now()
		   AND last_seen_at > now() - make_interval(secs => $2::float8)
		 RETURNING user_id::text`,
		HashToken(raw), s.idleTTL.Seconds()).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidSession
	}
	return userID, err
}

func (s *SessionStore) Delete(ctx context.Context, raw string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, HashToken(raw))
	return err
}

func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1::uuid`, userID)
	return err
}

// RunCleanup periodically removes dead sessions and old email tokens.
func (s *SessionStore) RunCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.db.Exec(ctx,
				`DELETE FROM sessions
				 WHERE expires_at < now() OR last_seen_at < now() - make_interval(secs => $1::float8)`,
				s.idleTTL.Seconds())
			_, _ = s.db.Exec(ctx,
				`DELETE FROM email_tokens WHERE expires_at < now() - interval '7 days'`)
		}
	}
}

// ---------- one-time email tokens ----------

const (
	PurposeVerifyEmail   = "verify_email"
	PurposeResetPassword = "reset_password"
)

var ErrInvalidToken = errors.New("invalid or expired token")

type EmailTokenStore struct{ db *pgxpool.Pool }

func NewEmailTokenStore(db *pgxpool.Pool) *EmailTokenStore { return &EmailTokenStore{db: db} }

// Create voids older unused tokens of the same purpose, then issues a new one.
func (s *EmailTokenStore) Create(ctx context.Context, userID, purpose string, ttl time.Duration) (string, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err = s.db.Exec(ctx,
		`UPDATE email_tokens SET used_at = now()
		 WHERE user_id = $1::uuid AND purpose = $2 AND used_at IS NULL`,
		userID, purpose); err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
		 VALUES ($1::uuid, $2, $3, $4)`,
		userID, purpose, hash, time.Now().Add(ttl))
	return raw, err
}

// Consume atomically marks the token used and returns its owner.
func (s *EmailTokenStore) Consume(ctx context.Context, raw, purpose string) (string, error) {
	var userID string
	err := s.db.QueryRow(ctx,
		`UPDATE email_tokens SET used_at = now()
		 WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id::text`,
		HashToken(raw), purpose).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidToken
	}
	return userID, err
}