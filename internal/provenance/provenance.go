// Package provenance contains the ForgeMemo-side verification boundary for
// SPIF artifacts. SPIF remains the canonical binary envelope; this package
// models the fields ForgeMemo must check before accepting an artifact into a
// project-scoped ledger.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/forge/forge/internal/security"
)

type Envelope struct {
	Version           string `json:"version"`
	ArtifactID        string `json:"artifact_id"`
	OwnerID           string `json:"owner_id"`
	BoundaryID        string `json:"boundary_id"`
	ContentDigest     string `json:"content_digest"`
	SignerID          string `json:"signer_id"`
	TaskID            string `json:"task_id"`
	Attempt           int    `json:"attempt"`
	Nonce             string `json:"nonce"`
	Timestamp         int64  `json:"timestamp"`
	PredecessorDigest string `json:"predecessor_digest,omitempty"`
	Signature         string `json:"signature"`
}

func digest(payload []byte) string { sum := sha256.Sum256(payload); return hex.EncodeToString(sum[:]) }

func canonical(e Envelope) string {
	e.Signature = ""
	b, _ := json.Marshal(e)
	return string(b)
}

func NewEnvelope(key []byte, ownerID, boundaryID, signerID, taskID, nonce string, attempt int, timestamp time.Time, payload []byte) (Envelope, error) {
	if ownerID == "" || boundaryID == "" || signerID == "" || taskID == "" || nonce == "" || len(payload) == 0 {
		return Envelope{}, fmt.Errorf("provenance envelope missing required scope or payload")
	}
	e := Envelope{Version: "forge-spif-bridge-v1", ArtifactID: digest(payload), OwnerID: ownerID, BoundaryID: boundaryID, ContentDigest: digest(payload), SignerID: signerID, TaskID: taskID, Attempt: attempt, Nonce: nonce, Timestamp: timestamp.UnixMilli()}
	e.Signature = security.Sign(key, canonical(e))
	return e, nil
}

func (e Envelope) VerifySignature(key []byte) bool {
	return security.Verify(key, canonical(e), e.Signature)
}

func (e Envelope) VerifyScope(ownerID, boundaryID, contentDigest string) error {
	if e.Version != "forge-spif-bridge-v1" {
		return fmt.Errorf("unsupported provenance version %q", e.Version)
	}
	if e.OwnerID != ownerID || e.BoundaryID != boundaryID {
		return fmt.Errorf("provenance scope mismatch")
	}
	if e.ContentDigest != contentDigest || e.ArtifactID != contentDigest {
		return fmt.Errorf("provenance content mismatch")
	}
	if e.Attempt < 0 || e.Nonce == "" || e.TaskID == "" {
		return fmt.Errorf("invalid replay identity")
	}
	return nil
}

type signerWindow struct {
	key                 []byte
	activeAt, revokedAt int64
}

type KeyRotation struct {
	OldSigner   string `json:"old_signer"`
	NewSigner   string `json:"new_signer"`
	EffectiveAt int64  `json:"effective_at"`
	Signature   string `json:"signature"`
}

func rotationCanonical(r KeyRotation) string {
	r.Signature = ""
	b, _ := json.Marshal(r)
	return string(b)
}

type TrustRegistry struct {
	mu      sync.RWMutex
	signers map[string]signerWindow
}

func NewTrustRegistry() *TrustRegistry { return &TrustRegistry{signers: make(map[string]signerWindow)} }

func (r *TrustRegistry) Add(signerID string, key []byte, activeAt time.Time) error {
	if signerID == "" || len(key) == 0 {
		return fmt.Errorf("signer and key are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signers[signerID] = signerWindow{key: append([]byte(nil), key...), activeAt: activeAt.UnixMilli()}
	return nil
}

func (r *TrustRegistry) Revoke(signerID string, revokedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.signers[signerID]
	if !ok {
		return fmt.Errorf("unknown signer %q", signerID)
	}
	s.revokedAt = revokedAt.UnixMilli()
	r.signers[signerID] = s
	return nil
}

// Rotate records an authenticated old-key-to-new-key transition and activates
// the successor only at the declared effective time.
func (r *TrustRegistry) Rotate(oldSigner, newSigner string, newKey []byte, effectiveAt time.Time) (KeyRotation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.signers[oldSigner]
	if !ok {
		return KeyRotation{}, fmt.Errorf("unknown old signer %q", oldSigner)
	}
	if newSigner == "" || len(newKey) == 0 {
		return KeyRotation{}, fmt.Errorf("new signer and key are required")
	}
	rotation := KeyRotation{OldSigner: oldSigner, NewSigner: newSigner, EffectiveAt: effectiveAt.UnixMilli()}
	rotation.Signature = security.Sign(old.key, rotationCanonical(rotation))
	r.signers[newSigner] = signerWindow{key: append([]byte(nil), newKey...), activeAt: rotation.EffectiveAt}
	return rotation, nil
}

func (r *TrustRegistry) VerifyRotation(rotation KeyRotation) error {
	r.mu.RLock()
	old, ok := r.signers[rotation.OldSigner]
	r.mu.RUnlock()
	if !ok || !security.Verify(old.key, rotationCanonical(rotation), rotation.Signature) {
		return fmt.Errorf("invalid key rotation")
	}
	return nil
}

func (r *TrustRegistry) Verify(e Envelope, ownerID, boundaryID string, now time.Time, maxAge time.Duration) error {
	if err := e.VerifyScope(ownerID, boundaryID, e.ContentDigest); err != nil {
		return err
	}
	r.mu.RLock()
	s, ok := r.signers[e.SignerID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("untrusted signer %q", e.SignerID)
	}
	if e.Timestamp < s.activeAt {
		return fmt.Errorf("artifact predates signer activation")
	}
	if s.revokedAt != 0 && e.Timestamp >= s.revokedAt {
		return fmt.Errorf("signer revoked at artifact timestamp")
	}
	when := time.UnixMilli(e.Timestamp)
	if when.After(now.Add(2 * time.Minute)) {
		return fmt.Errorf("artifact timestamp is in the future")
	}
	if now.Sub(when) > maxAge {
		return fmt.Errorf("artifact is stale")
	}
	if !e.VerifySignature(s.key) {
		return fmt.Errorf("invalid provenance signature")
	}
	return nil
}

type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func NewReplayGuard() *ReplayGuard { return &ReplayGuard{seen: make(map[string]struct{})} }

func (g *ReplayGuard) Accept(e Envelope) error {
	token := e.SignerID + "|" + e.TaskID + "|" + fmt.Sprint(e.Attempt) + "|" + e.Nonce
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.seen[token]; exists {
		return fmt.Errorf("provenance replay detected")
	}
	g.seen[token] = struct{}{}
	return nil
}
