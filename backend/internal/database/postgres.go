package database

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	port := strings.TrimSpace(os.Getenv("DB_PORT"))
	user := os.Getenv("DB_USER")
	password := os.Getenv("DB_PASSWORD")
	name := strings.TrimSpace(os.Getenv("DB_NAME"))
	sslmode := strings.TrimSpace(os.Getenv("DB_SSLMODE"))

	if host == "" || port == "" || user == "" || name == "" {
		return nil, fmt.Errorf("database configuration is incomplete")
	}

	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("DB_PORT must be a valid port number")
	}

	if sslmode == "" {
		return nil, fmt.Errorf("DB_SSLMODE must be configured")
	}

	// Build the URL safely, including passwords with special characters.
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + name,
	}

	query := url.Values{}
	query.Set("sslmode", sslmode)
	u.RawQuery = query.Encode()

	config, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}

	config.MaxConns = 10
	config.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	// Opening a pool alone does not prove the database is reachable.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}

	return pool, nil
}
