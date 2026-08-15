package db

import (
	"bytes"
	"testing"
)

func TestMemoryContractStoresTypedRevisionAndPreservesBoundary(t *testing.T) {
	database, err := Open(t.TempDir() + "/memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m := &MemoryRecord{OwnerID: "owner-1", BoundaryID: "project-alpha", Kind: MemoryEvidence, SourceActor: "user", SourceType: "conversation", Content: []byte("bounded retries"), SPIFEnvelope: []byte("opaque-spif-envelope"), Confidence: 1, Freshness: 1, SourceReliability: 1}
	if err := database.InsertMemoryRecord(m); err != nil {
		t.Fatal(err)
	}
	got, err := database.MemoryRevision("owner-1", "project-alpha", m.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Kind != MemoryEvidence || !bytes.Equal(got.Content, m.Content) {
		t.Fatalf("round trip = %+v", got)
	}
	if other, err := database.MemoryRevision("owner-1", "project-beta", m.RevisionID); err != nil {
		t.Fatal(err)
	} else if other != nil {
		t.Fatal("memory crossed boundary")
	}
}

func TestMemoryContractRejectsDigestAndScoreViolations(t *testing.T) {
	database, err := Open(t.TempDir() + "/memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m := &MemoryRecord{OwnerID: "owner", BoundaryID: "project", Kind: MemoryBelief, SourceActor: "agent", SourceType: "derivation", Content: []byte("claim"), ContentDigest: "wrong", SPIFEnvelope: []byte("spif"), Confidence: 1.2}
	if err := database.InsertMemoryRecord(m); err == nil {
		t.Fatal("invalid digest/score accepted")
	}
}

func TestCorrectionAndRetrievalContractsStayScoped(t *testing.T) {
	database, err := Open(t.TempDir() + "/memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first := &MemoryRecord{OwnerID: "owner", BoundaryID: "alpha", Kind: MemoryAssertion, SourceActor: "user", SourceType: "correction", Content: []byte("old"), SPIFEnvelope: []byte("spif-1"), Confidence: .8}
	if err := database.InsertMemoryRecord(first); err != nil {
		t.Fatal(err)
	}
	second := &MemoryRecord{OwnerID: "owner", BoundaryID: "alpha", Kind: MemoryAssertion, SourceActor: "user", SourceType: "correction", Content: []byte("new"), SPIFEnvelope: []byte("spif-2"), Confidence: .9, SupersedesRevisionID: first.RevisionID}
	if err := database.InsertMemoryRecord(second); err != nil {
		t.Fatal(err)
	}
	correction := &CorrectionEvent{OwnerID: "owner", BoundaryID: "alpha", TargetRevisionID: first.RevisionID, ReplacementRevisionID: second.RevisionID, Action: "amend", ReasonCode: "factual_error", ActorID: "user", SPIFEnvelope: []byte("spif-correction")}
	if err := database.InsertCorrection(correction); err != nil {
		t.Fatal(err)
	}
	cross := &CorrectionEvent{OwnerID: "owner", BoundaryID: "beta", TargetRevisionID: first.RevisionID, Action: "retract", ReasonCode: "wrong_boundary", ActorID: "user", SPIFEnvelope: []byte("spif-cross")}
	if err := database.InsertCorrection(cross); err == nil {
		t.Fatal("cross-boundary correction accepted")
	}
	if err := database.InsertRetrievalEvent(&RetrievalEvent{OwnerID: "owner", BoundaryID: "alpha", QueryDigest: "q1", RankingVersion: "v1", SelectedRevisionIDs: []string{second.RevisionID}}); err != nil {
		t.Fatal(err)
	}
}
