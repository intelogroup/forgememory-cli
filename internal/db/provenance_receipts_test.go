package db

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/forge/forge/internal/provenance"
)

func TestAcceptProvenanceReceiptIsScopedAndIdempotent(t *testing.T) {
	database, err := Open(t.TempDir() + "/provenance.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	key := []byte("test-key-not-random-32-bytes!!!!")
	registry := provenance.NewTrustRegistry()
	if err := registry.Add("device-a", key, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	replay := provenance.NewReplayGuard()
	envelope, err := provenance.NewEnvelope(key, "owner", "alpha", "device-a", "task-1", "nonce-1", 0, now, []byte("claim"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AcceptProvenanceReceipt("owner", "alpha", raw, registry, replay, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := database.AcceptProvenanceReceipt("owner", "alpha", raw, registry, replay, now, time.Hour); err == nil {
		t.Fatal("duplicate receipt accepted")
	}
	if err := database.AcceptProvenanceReceipt("owner", "beta", raw, registry, provenance.NewReplayGuard(), now, time.Hour); err == nil {
		t.Fatal("cross-boundary receipt accepted")
	}
}
