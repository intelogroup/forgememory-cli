package security

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/zalando/go-keyring"
)

// useIsolatedKeyForTests points key storage (keychain entry + file
// fallback) at a throwaway, per-test location and restores the real ones
// on cleanup. RotateKey unconditionally overwrites whatever entry
// keyringService/keyringUser name — running it against the real "forge-cli"
// entry from a test would wipe the signing key backing a real user's
// already-signed principles in their actual ~/.forge/forge.db.
func useIsolatedKeyForTests(t *testing.T) {
	t.Helper()
	origService, origUser, origHome := keyringService, keyringUser, testHomeOverride
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	id := hex.EncodeToString(suffix)
	keyringService = "forge-cli-test-" + id
	keyringUser = "forge-hmac-key-test-" + id
	testHomeOverride = t.TempDir()
	t.Cleanup(func() {
		_ = keyring.Delete(keyringService, keyringUser)
		keyringService, keyringUser, testHomeOverride = origService, origUser, origHome
	})
}

func TestSignVerifyRoundTrip(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	sig := Sign(key, "title\x00narrative")
	if !Verify(key, "title\x00narrative", sig) {
		t.Fatal("Verify rejected a signature it just produced")
	}
}

func TestVerifyRejectsTamperedData(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	sig := Sign(key, "title\x00narrative")
	if Verify(key, "title\x00tampered-narrative", sig) {
		t.Fatal("Verify accepted a signature for data it wasn't computed over")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	sig := Sign([]byte("key-a-32-bytes-long-padding!!!!!"), "payload")
	if Verify([]byte("key-b-32-bytes-long-padding!!!!!"), "payload", sig) {
		t.Fatal("Verify accepted a signature made with a different key")
	}
}

func TestRotateKeyChangesTheStoredKey(t *testing.T) {
	useIsolatedKeyForTests(t)

	before, _, err := GetOrCreateKey()
	if err != nil {
		t.Skipf("no key backend available: %v", err)
	}

	rotated, err := RotateKey()
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if len(rotated) != 32 {
		t.Fatalf("rotated key length = %d, want 32", len(rotated))
	}
	if string(rotated) == string(before) {
		t.Fatal("RotateKey returned the same key instead of a fresh one")
	}

	after, _, err := GetOrCreateKey()
	if err != nil {
		t.Fatalf("GetOrCreateKey after rotate: %v", err)
	}
	if string(after) != string(rotated) {
		t.Fatal("GetOrCreateKey after RotateKey returned a different key than RotateKey produced — rotation didn't persist")
	}
}

func TestVerifyRejectsMalformedSignature(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	if Verify(key, "payload", "not-hex") {
		t.Fatal("Verify accepted a non-hex signature")
	}
	if Verify(key, "payload", "") {
		t.Fatal("Verify accepted an empty signature")
	}
}
