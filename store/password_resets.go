package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resetTokenExpiry is the lifetime of password reset and account activation tokens.
// 24 hours is intentionally generous to cover the activation use case where an admin
// generates a link and shares it manually (e.g. in person or via external channel).
const resetTokenExpiry = 24 * time.Hour

// CreatePasswordResetToken generates a cryptographically random token,
// stores its SHA-256 hash, and returns the raw token for delivery to the user.
func CreatePasswordResetToken(ctx context.Context, pool *pgxpool.Pool, userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(raw)
	hash := hashToken(token)

	_, err := pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, userID, hash, time.Now().UTC().Add(resetTokenExpiry))
	if err != nil {
		return "", fmt.Errorf("insert token: %w", err)
	}
	return token, nil
}

// ValidatePasswordResetToken checks if a token is valid (exists, not used, not expired).
// Returns the associated user ID if valid.
func ValidatePasswordResetToken(ctx context.Context, pool *pgxpool.Pool, rawToken string) (string, error) {
	hash := hashToken(rawToken)
	var userID string
	err := pool.QueryRow(ctx, `
		SELECT user_id FROM password_reset_tokens
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
	`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// MarkTokenUsed marks a token as used so it cannot be reused.
// Returns ErrNotFound if the token was not found or was already used.
func MarkTokenUsed(ctx context.Context, pool *pgxpool.Pool, rawToken string) error {
	hash := hashToken(rawToken)
	tag, err := pool.Exec(ctx, `
		UPDATE password_reset_tokens SET used = TRUE WHERE token_hash = $1 AND used = FALSE
	`, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ConsumePasswordResetToken atomically validates and marks a token as used.
// Returns the associated user ID if the token was valid and not yet used.
// Returns ErrNotFound if the token is invalid, expired, or already used.
// Prefer this over calling ValidatePasswordResetToken + MarkTokenUsed separately.
func ConsumePasswordResetToken(ctx context.Context, pool *pgxpool.Pool, rawToken string) (string, error) {
	hash := hashToken(rawToken)
	var userID string
	err := pool.QueryRow(ctx, `
		UPDATE password_reset_tokens
		SET used = TRUE
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
		RETURNING user_id
	`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// hashToken returns the SHA-256 hex digest of a raw token string.
// Always call on raw tokens only — never on an already-hashed value.
func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
