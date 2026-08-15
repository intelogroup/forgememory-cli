package provenance

import (
	"testing"
	"time"
)

func TestEnvelopeTrustScopeAndReplay(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	now := time.Now().UTC()
	e, err := NewEnvelope(key, "owner", "alpha", "device-a", "task-1", "nonce-1", 0, now, []byte("claim"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewTrustRegistry()
	if err := registry.Add("device-a", key, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Verify(e, "owner", "alpha", now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := registry.Verify(e, "owner", "beta", now, time.Hour); err == nil {
		t.Fatal("cross-boundary artifact accepted")
	}
	guard := NewReplayGuard()
	if err := guard.Accept(e); err != nil {
		t.Fatal(err)
	}
	if err := guard.Accept(e); err == nil {
		t.Fatal("replayed artifact accepted")
	}
}

func TestEnvelopeRejectsTamperingStaleAndRevokedSigner(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	now := time.Now().UTC()
	e, err := NewEnvelope(key, "owner", "alpha", "device-a", "task-1", "nonce-1", 0, now, []byte("claim"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewTrustRegistry()
	if err := registry.Add("device-a", key, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	tampered := e
	tampered.BoundaryID = "beta"
	if err := registry.Verify(tampered, "owner", "alpha", now, time.Hour); err == nil {
		t.Fatal("tampered scope accepted")
	}
	stale := e
	stale.Timestamp = now.Add(-2 * time.Hour).UnixMilli()
	if err := registry.Verify(stale, "owner", "alpha", now, time.Hour); err == nil {
		t.Fatal("stale artifact accepted")
	}
	if err := registry.Revoke("device-a", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Verify(e, "owner", "alpha", now, time.Hour); err == nil {
		t.Fatal("revoked signer accepted")
	}
}

func TestTrustRegistryAuthenticatesKeyRotation(t *testing.T) {
	now := time.Now().UTC()
	oldKey := []byte("old-key-not-random-32-bytes!!!!!!")
	newKey := []byte("new-key-not-random-32-bytes!!!!!!")
	registry := NewTrustRegistry()
	if err := registry.Add("old", oldKey, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rotation, err := registry.Rotate("old", "new", newKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyRotation(rotation); err != nil {
		t.Fatal(err)
	}
	rotation.NewSigner = "attacker"
	if err := registry.VerifyRotation(rotation); err == nil {
		t.Fatal("forged rotation accepted")
	}
}
