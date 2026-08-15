package security

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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

func TestGetOrCreateKeySkipsKeychainAfterPersistentFailure(t *testing.T) {
	useIsolatedKeyForTests(t)

	origGet, origSet := keyringGet, keyringSet
	defer func() { keyringGet, keyringSet = origGet, origSet }()
	getCalls, setCalls := 0, 0
	keyringGet = func(string, string) (string, error) {
		getCalls++
		return "", errors.New("keychain not found")
	}
	keyringSet = func(string, string, string) error {
		setCalls++
		return errors.New("keychain not found")
	}

	first, usedFallback, err := GetOrCreateKey()
	if err != nil || !usedFallback || len(first) != 32 {
		t.Fatalf("first lookup = (%d bytes, fallback=%v, err=%v), want fallback key", len(first), usedFallback, err)
	}
	if getCalls != 1 || setCalls != 1 {
		t.Fatalf("first lookup called keychain get=%d set=%d, want one each", getCalls, setCalls)
	}

	getCalls, setCalls = 0, 0
	second, usedFallback, err := GetOrCreateKey()
	if err != nil || !usedFallback || string(second) != string(first) {
		t.Fatalf("second lookup = (%d bytes, fallback=%v, err=%v), want same fallback key", len(second), usedFallback, err)
	}
	if getCalls != 0 || setCalls != 0 {
		t.Fatalf("second lookup called keychain get=%d set=%d, want no calls after persistent failure", getCalls, setCalls)
	}
}

// TestSetKeyringIdentityForTests_RepointsAwayFromRealEntry guards the
// mechanism cmd's TestMain relies on to keep `forge doctor` tests (which
// call GetOrCreateKey indirectly) from ever touching the real "forge-cli"
// keychain entry.
func TestSetKeyringIdentityForTests_RepointsAwayFromRealEntry(t *testing.T) {
	origService, origUser := keyringService, keyringUser
	defer func() { keyringService, keyringUser = origService, origUser }()

	id, cleanup := SetKeyringIdentityForTests()
	defer cleanup()

	if keyringService == "forge-cli" || keyringUser == "forge-hmac-key" {
		t.Fatalf("SetKeyringIdentityForTests left the real entry name in place: service=%q user=%q", keyringService, keyringUser)
	}
	if id == "" {
		t.Fatal("expected a non-empty test ID")
	}
}

// TestKeyringIdentityEnvOverride guards the subprocess-isolation half of the
// same mechanism: a real `forge` binary spawned by an integration test
// reads FORGE_TEST_KEYRING_ID at package init and must not fall back to the
// real "forge-cli"/"forge-hmac-key" entry names when it's set.
func TestKeyringIdentityEnvOverride(t *testing.T) {
	origService, origUser := keyringService, keyringUser
	defer func() { keyringService, keyringUser = origService, origUser }()
	keyringService, keyringUser = "forge-cli", "forge-hmac-key"

	t.Setenv(forgeTestKeyringIDEnv, "test-env-id-12345")
	applyTestKeyringIDEnv()

	if keyringService != "forge-cli-test-test-env-id-12345" {
		t.Errorf("keyringService = %q, want forge-cli-test-test-env-id-12345", keyringService)
	}
	if keyringUser != "forge-hmac-key-test-test-env-id-12345" {
		t.Errorf("keyringUser = %q, want forge-hmac-key-test-test-env-id-12345", keyringUser)
	}
}

// TestKeyringIdentityEnvOverride_EmptyLeavesIdentityUnchanged confirms an
// unset/empty override doesn't clobber a keyringService/keyringUser another
// test (or SetKeyringIdentityForTests) already set.
func TestKeyringIdentityEnvOverride_EmptyLeavesIdentityUnchanged(t *testing.T) {
	origService, origUser := keyringService, keyringUser
	defer func() { keyringService, keyringUser = origService, origUser }()
	keyringService, keyringUser = "forge-cli-test-existing", "forge-hmac-key-test-existing"

	t.Setenv(forgeTestKeyringIDEnv, "")
	applyTestKeyringIDEnv()

	if keyringService != "forge-cli-test-existing" || keyringUser != "forge-hmac-key-test-existing" {
		t.Errorf("empty env override changed identity: service=%q user=%q", keyringService, keyringUser)
	}
}

func TestGetOrCreateKey_TestSubprocessUsesFileFallback(t *testing.T) {
	useIsolatedKeyForTests(t)
	t.Setenv(forgeTestKeyringIDEnv, "isolated-subprocess")

	origGet, origSet := keyringGet, keyringSet
	defer func() { keyringGet, keyringSet = origGet, origSet }()
	keyringGet = func(string, string) (string, error) {
		t.Fatal("test subprocess key identity must not probe the OS keychain")
		return "", nil
	}
	keyringSet = func(string, string, string) error {
		t.Fatal("test subprocess key identity must not write the OS keychain")
		return nil
	}

	key, usedFallback, err := GetOrCreateKey()
	if err != nil || !usedFallback || len(key) != 32 {
		t.Fatalf("GetOrCreateKey = (%d bytes, fallback=%v, err=%v), want isolated file fallback", len(key), usedFallback, err)
	}
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
