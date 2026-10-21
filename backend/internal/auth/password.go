package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordLength = 8
	MaxPasswordLength = 128 // stops anyone feeding megabytes into Argon2

	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	saltLen             = 16
)

var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
)

// Limits concurrent hashing so a burst of requests cannot exhaust memory.
var hashSem = make(chan struct{}, 4)

// Verified against when a login names an unknown email, so response time
// does not reveal whether an account exists.
var dummyHash, _ = HashPassword("dummy-password-for-timing")

func ValidatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if n > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

func HashPassword(p string) (string, error) {
	hashSem <- struct{}{}
	defer func() { <-hashSem }()

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(p, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported password hash format")
	}
	var m, t, th uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	hashSem <- struct{}{}
	defer func() { <-hashSem }()

	got := argon2.IDKey([]byte(p), salt, t, m, uint8(th), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}