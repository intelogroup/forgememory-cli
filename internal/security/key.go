// Package security manages the HMAC key used to sign and verify principles
// stored in ~/.forge/forge.db. The key is stored in the OS keychain
// (Keychain on macOS, Credential Manager on Windows, Secret Service on
// Linux) when available. A local attacker with filesystem write access to
// forge.db cannot also read the OS keychain without separate authorization,
// so this is the precondition that makes HMAC signing meaningful — a key
// stored next to the data it protects gives no security at all.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// keychainTimeout bounds how long we wait on the OS keychain before falling
// back to the file-based key. An unsigned/new binary can trigger an
// interactive "Allow access?" prompt on macOS; a blocked daemon event loop
// is worse than a session running temporarily unsigned.
const keychainTimeout = 500 * time.Millisecond

// keychainRetryAfter prevents every short-lived hook process from probing a
// keychain backend that has already failed. The marker contains no secret and
// expires so a keychain made available later is still detected.
const keychainRetryAfter = 10 * time.Minute

// keyringService/keyringUser identify the real keychain entry and are vars
// (not consts) only so tests can point RotateKey at a throwaway entry via
// UseIsolatedKeyForTests — RotateKey overwrites whatever entry these name,
// and doing that against the real "forge-cli" entry from a test would wipe
// the signing key of every principle already stored in the user's real
// ~/.forge/forge.db.
var (
	keyringService = "forge-cli"
	keyringUser    = "forge-hmac-key"
	keyringGet     = keyring.Get
	keyringSet     = keyring.Set
)

// forgeTestKeyringIDEnv, when set, repoints the keychain entry name away
// from the real "forge-cli" one for this process. It exists so a *separate*
// process — a real `forge` binary spawned as a subprocess by an integration
// test (e.g. cmd/integration_test.go's runForgePath) — can be isolated too;
// SetKeyringIdentityForTests only affects the calling process's own vars,
// which a subprocess doesn't inherit.
const forgeTestKeyringIDEnv = "FORGE_TEST_KEYRING_ID"

func init() {
	applyTestKeyringIDEnv()
}

// applyTestKeyringIDEnv is init()'s logic, split out so tests can exercise
// it directly (init() itself only ever runs once, at process start, before
// t.Setenv could take effect).
func applyTestKeyringIDEnv() {
	if id := os.Getenv(forgeTestKeyringIDEnv); id != "" {
		keyringService = "forge-cli-test-" + id
		keyringUser = "forge-hmac-key-test-" + id
	}
}

const (
	keyFileName        = "forge.key"
	keychainMarkerName = "keychain-unavailable"
	keychainProbeLock  = "keychain-probe.lock"
)

// GetOrCreateKey returns the HMAC signing key, creating one on first use.
// It tries the OS keychain first; if unavailable (headless Linux with no
// Secret Service, etc.) it falls back to a chmod-600 file under ~/.forge
// and reports that fallback via usedFallback so callers can warn the user.
func GetOrCreateKey() (key []byte, usedFallback bool, err error) {
	// Integration tests spawn real forge subprocesses. They pass a unique
	// keyring identity so those processes cannot touch a developer's real
	// keychain entry. In headless Linux CI, probing Secret Service can still
	// block inside the D-Bus client even though the caller has a timeout. Test
	// subprocesses therefore use the already-isolated file fallback directly;
	// production callers never set this variable.
	if os.Getenv(forgeTestKeyringIDEnv) != "" {
		goto fallback
	}
	if !keychainRecentlyUnavailable() {
		// Hook/daemon processes can start together. Only one process may probe
		// the keychain; peers use the fallback while that probe is in flight.
		// This prevents a burst of `security` helper processes and prompts.
		probeLock, acquired := acquireKeychainProbe()
		if acquired {
			defer releaseKeychainProbe(probeLock)
		} else {
			goto fallback
		}
		if secret, ok := keyringGetWithTimeout(); ok {
			decoded, decErr := hex.DecodeString(secret)
			if decErr == nil && len(decoded) == 32 {
				clearKeychainUnavailable()
				return decoded, false, nil
			}
			// Corrupt entry — regenerate below.
		}

		newKey := make([]byte, 32)
		if _, err := rand.Read(newKey); err != nil {
			return nil, false, fmt.Errorf("generate key: %w", err)
		}

		if keyringSetWithTimeout(hex.EncodeToString(newKey)) {
			clearKeychainUnavailable()
			return newKey, false, nil
		}
		markKeychainUnavailable()
	}

fallback:
	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		return nil, false, fmt.Errorf("generate key: %w", err)
	}

	// Keychain unavailable or too slow to respond (e.g. blocked on an
	// interactive access prompt) — fall back to a file, but reuse an existing one
	// instead of overwriting it (would invalidate every prior signature).
	path, err := keyFilePath()
	if err != nil {
		return nil, false, err
	}
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create key dir: %w", err)
	}
	if err := os.WriteFile(path, newKey, 0o600); err != nil {
		return nil, false, fmt.Errorf("write key file: %w", err)
	}
	return newKey, true, nil
}

// keyringGetWithTimeout wraps keyring.Get with a hard deadline. go-keyring's
// underlying OS calls take no context/cancellation, so a slow or prompting
// backend is abandoned rather than awaited — the goroutine may leak until
// the OS call returns, but that's a one-time cost bounded by sync.Once.
func keyringGetWithTimeout() (string, bool) {
	type result struct {
		val string
		ok  bool
	}
	ch := make(chan result, 1)
	go func() {
		secret, err := keyringGet(keyringService, keyringUser)
		ch <- result{secret, err == nil}
	}()
	select {
	case r := <-ch:
		return r.val, r.ok
	case <-time.After(keychainTimeout):
		return "", false
	}
}

func keyringSetWithTimeout(secret string) bool {
	ch := make(chan bool, 1)
	go func() {
		ch <- keyringSet(keyringService, keyringUser, secret) == nil
	}()
	select {
	case ok := <-ch:
		return ok
	case <-time.After(keychainTimeout):
		return false
	}
}

// SetKeyringIdentityForTests repoints the OS keychain entry name away from
// the real "forge-cli"/"forge-hmac-key" entry, for the remaining lifetime of
// the process, and returns the generated test ID plus a cleanup func that
// removes the throwaway entry. Call once from TestMain in any package whose
// tests exercise GetOrCreateKey indirectly (e.g. cmd's `forge doctor`), so
// they can never read or write a developer's real keychain-stored signing
// key. The file-fallback path needs no equivalent — it already isolates via
// HOME, which per-test setup in this repo already overrides.
//
// The returned ID should be passed to any real `forge` binary spawned as a
// test subprocess via the FORGE_TEST_KEYRING_ID env var — a subprocess is a
// separate process with its own copy of keyringService/keyringUser, so it
// isn't covered by this process's mutated vars, only by that env var (see
// the package-level init()).
func SetKeyringIdentityForTests() (id string, cleanup func()) {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	id = hex.EncodeToString(suffix)
	keyringService = "forge-cli-test-" + id
	keyringUser = "forge-hmac-key-test-" + id
	return id, func() { _ = keyring.Delete(keyringService, keyringUser) }
}

// RotateKey generates a fresh 32-byte key, overwrites the stored key
// (keychain if available, else the fallback file) unconditionally, and
// returns it so the caller can re-sign existing data before the old key is
// gone for good. Unlike GetOrCreateKey this never reuses an existing key.
func RotateKey() ([]byte, error) {
	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	storedInKeychain := !keychainRecentlyUnavailable() && keyringSetWithTimeout(hex.EncodeToString(newKey))
	if storedInKeychain {
		clearKeychainUnavailable()
	} else if !storedInKeychain {
		markKeychainUnavailable()
	}

	path, err := keyFilePath()
	if err != nil {
		return nil, err
	}
	// Always refresh the fallback file too when it exists, so a keychain
	// that becomes unavailable later doesn't resurrect the pre-rotation key.
	if _, statErr := os.Stat(path); statErr == nil || !storedInKeychain {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create key dir: %w", err)
		}
		if err := os.WriteFile(path, newKey, 0o600); err != nil {
			return nil, fmt.Errorf("write key file: %w", err)
		}
	}

	return newKey, nil
}

// testHomeOverride redirects the file-fallback path during tests; see
// UseIsolatedKeyForTests. Empty in production.
var testHomeOverride string

func keyFilePath() (string, error) {
	if testHomeOverride != "" {
		return filepath.Join(testHomeOverride, ".forge", keyFileName), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(home, ".forge", keyFileName), nil
}

func keychainMarkerPath() (string, error) {
	path, err := keyFilePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), keychainMarkerName), nil
}

func keychainRecentlyUnavailable() bool {
	path, err := keychainMarkerPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) < keychainRetryAfter
}

func markKeychainUnavailable() {
	path, err := keychainMarkerPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte("unavailable\n"), 0o600)
}

func clearKeychainUnavailable() {
	path, err := keychainMarkerPath()
	if err == nil {
		_ = os.Remove(path)
	}
}

func acquireKeychainProbe() (*os.File, bool) {
	path, err := keychainMarkerPath()
	if err != nil {
		return nil, false
	}
	lockPath := filepath.Join(filepath.Dir(path), keychainProbeLock)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, false
	}
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, false
	}
	return lock, true
}

func releaseKeychainProbe(lock *os.File) {
	if lock == nil {
		return
	}
	path := lock.Name()
	_ = lock.Close()
	_ = os.Remove(path)
}

// Sign computes HMAC-SHA256(data, key) and returns it hex-encoded.
func Sign(key []byte, data string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is the valid HMAC-SHA256 of data under key.
// Uses constant-time comparison to avoid timing side channels.
func Verify(key []byte, data, sig string) bool {
	if sig == "" {
		return false
	}
	expected, err := hex.DecodeString(Sign(key, data))
	if err != nil {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != len(expected) {
		return false
	}
	return hmac.Equal(expected, got)
}
