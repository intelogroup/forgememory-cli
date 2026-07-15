package security

import (
	"crypto/rand"
	"strings"
	"testing"
)

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := randKey(t)
	plaintext := "a poisoned narrative should never sit on disk in the clear"

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(ciphertext, EncPrefix) {
		t.Fatalf("expected ciphertext to carry %q prefix, got %q", EncPrefix, ciphertext)
	}
	if strings.Contains(ciphertext, plaintext) {
		t.Fatalf("ciphertext leaks plaintext: %q", ciphertext)
	}

	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plaintext {
		t.Fatalf("Decrypt roundtrip mismatch: got %q want %q", got, plaintext)
	}
}

func TestEncryptEmptyStringPassesThrough(t *testing.T) {
	key := randKey(t)
	ciphertext, err := Encrypt(key, "")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	if ciphertext != "" {
		t.Fatalf("expected empty plaintext to stay empty, got %q", ciphertext)
	}
}

func TestDecryptLegacyPlaintextPassesThrough(t *testing.T) {
	key := randKey(t)
	// Rows written before encryption was enabled carry no EncPrefix.
	got, err := Decrypt(key, "unencrypted legacy narrative")
	if err != nil {
		t.Fatalf("Decrypt legacy: %v", err)
	}
	if got != "unencrypted legacy narrative" {
		t.Fatalf("expected legacy plaintext passthrough, got %q", got)
	}
}

func TestDecryptFailsOnTamperedCiphertext(t *testing.T) {
	key := randKey(t)
	ciphertext, err := Encrypt(key, "sensitive")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a byte inside the base64 payload — simulates an attacker editing
	// the .db file's bytes directly.
	tampered := []byte(ciphertext)
	last := len(tampered) - 1
	if tampered[last] == 'A' {
		tampered[last] = 'B'
	} else {
		tampered[last] = 'A'
	}

	if _, err := Decrypt(key, string(tampered)); err == nil {
		t.Fatal("expected Decrypt to reject tampered ciphertext, got nil error")
	}
}

func TestDecryptFailsOnWrongKey(t *testing.T) {
	key := randKey(t)
	otherKey := randKey(t)

	ciphertext, err := Encrypt(key, "sensitive")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := Decrypt(otherKey, ciphertext); err == nil {
		t.Fatal("expected Decrypt under the wrong key to fail, got nil error")
	}
}
