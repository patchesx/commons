package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const encPrefix = "enc:v1:"

// Encrypt encrypts plaintext using AES-256-GCM and returns an "enc:v1:"-prefixed string
// safe to store in the database. A random 12-byte nonce is prepended to the ciphertext.
func Encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value produced by Encrypt. If the value does not carry the
// "enc:v1:" prefix it is returned as-is — this provides backward compatibility
// with legacy plaintext rows that predate encryption.
func Decrypt(key []byte, value string) (string, error) {
	if !strings.HasPrefix(value, encPrefix) {
		return value, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	nonceSz := gcm.NonceSize()
	if len(data) < nonceSz {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := data[:nonceSz], data[nonceSz:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
