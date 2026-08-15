package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// EncPrefix marks a stored value as AES-256-GCM ciphertext produced by
// Encrypt. Rows written before encryption was enabled (or when the key was
// unavailable at write time) carry no prefix; Decrypt treats those as legacy
// plaintext instead of failing, so turning encryption on doesn't corrupt
// existing principles/events.
const EncPrefix = "enc:v1:"

// deriveEncKey domain-separates the AES key from the HMAC signing key so the
// same 32-byte secret from internal/security isn't reused raw under two
// different primitives.
func deriveEncKey(key []byte) []byte {
	h := sha256.Sum256(append([]byte("forge-encrypt-v1:"), key...))
	return h[:]
}

// Encrypt seals plaintext with AES-256-GCM and returns it prefixed with
// EncPrefix, ready to store directly in a TEXT column. Empty input is
// returned unchanged — nothing to hide, and it keeps empty-narrative rows
// from round-tripping through AES for no reason.
func Encrypt(key []byte, plaintext string) (string, error) {
	return EncryptBound(key, plaintext, "")
}

// EncryptBound seals plaintext and authenticates the supplied context. The
// context is not secret; it binds ciphertext to its owner, boundary, record,
// or purpose so valid ciphertext cannot be attached elsewhere.
func EncryptBound(key []byte, plaintext, associatedData string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(associatedData))
	return EncPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. A value without EncPrefix is returned as-is:
// legacy plaintext from before encryption was enabled.
func Decrypt(key []byte, stored string) (string, error) {
	return DecryptBound(key, stored, "")
}

// DecryptBound reverses EncryptBound and rejects context rebinding.
func DecryptBound(key []byte, stored, associatedData string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, EncPrefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, EncPrefix))
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, []byte(associatedData))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(deriveEncKey(key))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
