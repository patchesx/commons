package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey returns a valid 32-byte AES-256 key for use in tests.
func testKey() []byte {
	return []byte("12345678901234567890123456789012")
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey()
	plaintext := "super secret value"

	ciphertext, err := Encrypt(key, plaintext)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ciphertext, "enc:v1:"))

	got, err := Decrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestEncryptProducesUniqueNonces(t *testing.T) {
	key := testKey()
	plaintext := "same plaintext"

	c1, err := Encrypt(key, plaintext)
	require.NoError(t, err)
	c2, err := Encrypt(key, plaintext)
	require.NoError(t, err)

	assert.NotEqual(t, c1, c2, "two encryptions of the same plaintext should differ due to random nonces")
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	key := testKey()

	ciphertext, err := Encrypt(key, "")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ciphertext, "enc:v1:"))

	got, err := Decrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestEncryptInvalidKeySize(t *testing.T) {
	_, err := Encrypt([]byte("tooshort"), "plaintext")
	assert.Error(t, err)
}

func TestDecryptLegacyPlaintext(t *testing.T) {
	// Values without the enc:v1: prefix are returned as-is (backward compat).
	got, err := Decrypt(testKey(), "not encrypted at all")
	require.NoError(t, err)
	assert.Equal(t, "not encrypted at all", got)
}

func TestDecryptEmptyString(t *testing.T) {
	got, err := Decrypt(testKey(), "")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestDecryptWrongKey(t *testing.T) {
	ciphertext, err := Encrypt(testKey(), "secret")
	require.NoError(t, err)

	wrongKey := []byte("98765432109876543210987654321098")
	_, err = Decrypt(wrongKey, ciphertext)
	assert.Error(t, err)
}

func TestDecryptTruncatedCiphertext(t *testing.T) {
	// enc:v1: prefix present but payload is shorter than a nonce (12 bytes).
	// base64("test") = "dGVzdA==" — only 4 bytes decoded, less than the 12-byte nonce.
	value := encPrefix + "dGVzdA=="
	_, err := Decrypt(testKey(), value)
	assert.Error(t, err)
}

func TestDecryptInvalidBase64(t *testing.T) {
	value := encPrefix + "not-valid-base64!!!"
	_, err := Decrypt(testKey(), value)
	assert.Error(t, err)
}
