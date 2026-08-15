package handoff

import (
	"testing"
	"time"
)

func TestSignedHandoffEnforcesScopeExpiryAndCapabilities(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	now := time.Now().UTC()
	parent, err := Sign(key, Envelope{OwnerID: "owner", BoundaryID: "alpha", SourceAgent: "research", TargetAgent: "review", Capabilities: []string{"read", "propose"}, Nonce: "n1", ExpiresAt: now.Add(time.Hour).UnixMilli(), DelegationDepth: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Verify(key, "owner", "alpha", now); err != nil {
		t.Fatal(err)
	}
	child, err := Sign(key, Envelope{OwnerID: "owner", BoundaryID: "alpha", SourceAgent: "review", TargetAgent: "execute", Capabilities: []string{"propose"}, ParentDigest: "parent-digest", Nonce: "n2", ExpiresAt: now.Add(time.Minute).UnixMilli(), DelegationDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := Delegate(parent, child); err != nil {
		t.Fatal(err)
	}
	child.Capabilities = []string{"execute"}
	if err := Delegate(parent, child); err == nil {
		t.Fatal("capability widening accepted")
	}
	child.Capabilities = []string{"propose"}
	child.BoundaryID = "beta"
	if err := Delegate(parent, child); err == nil {
		t.Fatal("cross-boundary delegation accepted")
	}
}
