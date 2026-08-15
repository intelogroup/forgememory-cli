// Package handoff defines a small signed delegation envelope for agents.
// It is intentionally capability-based and model/vendor neutral.
package handoff

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forge/forge/internal/security"
)

type Envelope struct {
	Version         string   `json:"version"`
	ID              string   `json:"id"`
	OwnerID         string   `json:"owner_id"`
	BoundaryID      string   `json:"boundary_id"`
	SourceAgent     string   `json:"source_agent"`
	TargetAgent     string   `json:"target_agent"`
	Capabilities    []string `json:"capabilities"`
	ParentDigest    string   `json:"parent_digest,omitempty"`
	Nonce           string   `json:"nonce"`
	ExpiresAt       int64    `json:"expires_at"`
	DelegationDepth int      `json:"delegation_depth"`
	ApprovalDigest  string   `json:"approval_digest,omitempty"`
	Signature       string   `json:"signature"`
}

func canonical(e Envelope) string { e.Signature = ""; b, _ := json.Marshal(e); return string(b) }

func Sign(key []byte, e Envelope) (Envelope, error) {
	if e.Version == "" {
		e.Version = "forge-handoff-v1"
	}
	if e.OwnerID == "" || e.BoundaryID == "" || e.SourceAgent == "" || e.TargetAgent == "" || e.Nonce == "" || e.ExpiresAt == 0 {
		return Envelope{}, fmt.Errorf("handoff missing required identity, scope, or expiry")
	}
	if e.DelegationDepth < 0 {
		return Envelope{}, fmt.Errorf("delegation depth cannot be negative")
	}
	e.Signature = security.Sign(key, canonical(e))
	return e, nil
}

func (e Envelope) Verify(key []byte, ownerID, boundaryID string, now time.Time) error {
	if e.Version != "forge-handoff-v1" || e.OwnerID != ownerID || e.BoundaryID != boundaryID {
		return fmt.Errorf("handoff scope or version mismatch")
	}
	if e.ExpiresAt <= now.UnixMilli() {
		return fmt.Errorf("handoff expired")
	}
	if e.DelegationDepth < 0 || e.Nonce == "" || len(e.Capabilities) == 0 {
		return fmt.Errorf("invalid handoff policy")
	}
	if !security.Verify(key, canonical(e), e.Signature) {
		return fmt.Errorf("invalid handoff signature")
	}
	return nil
}

func HasCapabilities(granted, requested []string) bool {
	set := make(map[string]struct{}, len(granted))
	for _, capability := range granted {
		set[strings.TrimSpace(capability)] = struct{}{}
	}
	for _, capability := range requested {
		if _, ok := set[strings.TrimSpace(capability)]; !ok {
			return false
		}
	}
	return true
}

func Delegate(parent Envelope, child Envelope) error {
	if parent.OwnerID != child.OwnerID || parent.BoundaryID != child.BoundaryID {
		return fmt.Errorf("delegation crosses scope")
	}
	if child.ParentDigest == "" {
		return fmt.Errorf("child handoff must name parent digest")
	}
	if child.DelegationDepth != parent.DelegationDepth+1 {
		return fmt.Errorf("invalid delegation depth")
	}
	if !HasCapabilities(parent.Capabilities, child.Capabilities) {
		return fmt.Errorf("delegation widens capabilities")
	}
	return nil
}
