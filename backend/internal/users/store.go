package users

import (
	"context"
	"errors"
	"strings"
	"time"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound   = errors.New("user not found")
	ErrEmailTaken = errors.New("email already registered")
)

type User struct {
	ID              string
	Email           string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
func localPart(email string) string {
	email = NormalizeEmail(email)
	return strings.SplitN(email, "@", 2)[0]
}

const cols = `id::text, email, password_hash, email_verified_at, created_at`

func scan(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerifiedAt, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// func (s *Store) Create(ctx context.Context, email, passwordHash string) (*User, error) {
// 	u, err := scan(s.db.QueryRow(ctx,
// 		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING `+cols,
// 		email, passwordHash))
// 	var pgErr *pgconn.PgError
// 	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
// 		return nil, ErrEmailTaken
// 	}
// 	return u, err
// }



func (s *Store) Create(ctx context.Context, email, passwordHash string) (*User, error) {
	email = NormalizeEmail(email)

	base := localPart(email)
	name := base

	for i := 0; ; i++ {
		u, err := scan(s.db.QueryRow(ctx,
			`INSERT INTO users (email, display_name, password_hash)
			 VALUES ($1, $2, $3)
			 RETURNING `+cols,
			email, name, passwordHash,
		))
		if err == nil {
			return u, nil
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Only retry when the display-name index caused the conflict.
			if pgErr.ConstraintName == "users_display_name_ci_idx" ||
				pgErr.ConstraintName == "users_display_name_ci_idx_key" {
				i++
				name = fmt.Sprintf("%s%d", base, i)
				continue
			}

			return nil, ErrEmailTaken
		}

		return nil, err
	}
}


func (s *Store) GetByEmail(ctx context.Context, email string) (*User, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM users WHERE email = $1`, email))
}

func (s *Store) GetByID(ctx context.Context, id string) (*User, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM users WHERE id = $1::uuid`, id))
}

func (s *Store) MarkEmailVerified(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
		 WHERE id = $1::uuid`, id)
	return err
}

func (s *Store) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1::uuid`,
		id, passwordHash)
	return err
}