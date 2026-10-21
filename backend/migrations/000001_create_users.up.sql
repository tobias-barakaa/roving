-- gen_random_uuid() is built in since PostgreSQL 13; no extension needed.
CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text NOT NULL,
    password_hash     text NOT NULL,
    email_verified_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_key       UNIQUE (email),
    CONSTRAINT users_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT users_email_length    CHECK (char_length(email) BETWEEN 3 AND 254)
);