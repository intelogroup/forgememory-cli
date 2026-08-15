package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/forge/forge/internal/provenance"
	"github.com/google/uuid"
)

// AcceptProvenanceReceipt verifies an envelope before recording its durable
// acceptance. The database uniqueness constraint makes imports idempotent and
// prevents the same task attempt/nonce from producing a second receipt.
func (d *DB) AcceptProvenanceReceipt(ownerID, boundaryID string, raw []byte, registry *provenance.TrustRegistry, replay *provenance.ReplayGuard, now time.Time, maxAge time.Duration) error {
	var envelope provenance.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode provenance envelope: %w", err)
	}
	if registry == nil || replay == nil {
		return fmt.Errorf("provenance registry and replay guard are required")
	}
	if err := registry.Verify(envelope, ownerID, boundaryID, now, maxAge); err != nil {
		return err
	}
	if err := replay.Accept(envelope); err != nil {
		return err
	}
	_, err := d.conn.Exec(`INSERT INTO provenance_receipts
		(id,owner_id,boundary_id,artifact_id,signer_id,task_id,attempt,nonce,timestamp,envelope)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, uuid.New().String(), ownerID, boundaryID,
		envelope.ArtifactID, envelope.SignerID, envelope.TaskID, envelope.Attempt,
		envelope.Nonce, envelope.Timestamp, raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return err
		}
		return fmt.Errorf("store provenance receipt: %w", err)
	}
	return nil
}
