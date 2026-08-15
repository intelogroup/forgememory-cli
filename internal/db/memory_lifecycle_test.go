package db

import (
	"bytes"
	"testing"
)

func TestSearchMemoryAuthorizesBeforeRankingAndDeletionHidesContent(t *testing.T) {
	database, err := Open(t.TempDir() + "/lifecycle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	alpha := &MemoryRecord{OwnerID: "owner", BoundaryID: "alpha", Kind: MemoryEvidence, SourceActor: "user", SourceType: "note", Content: []byte("alpha secret retry"), SPIFEnvelope: []byte("spif-alpha"), Confidence: .9, Freshness: 1, SourceReliability: 1}
	beta := &MemoryRecord{OwnerID: "owner", BoundaryID: "beta", Kind: MemoryEvidence, SourceActor: "user", SourceType: "note", Content: []byte("beta secret retry"), SPIFEnvelope: []byte("spif-beta"), Confidence: 1, Freshness: 1, SourceReliability: 1}
	if err := database.InsertMemoryRecord(alpha); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertMemoryRecord(beta); err != nil {
		t.Fatal(err)
	}
	results, err := database.SearchMemory("owner", "alpha", "retry", 10)
	if err != nil || len(results) != 1 || results[0].Record.BoundaryID != "alpha" {
		t.Fatalf("scoped search = %+v, %v", results, err)
	}
	if err := database.DeleteMemoryRevision("owner", "alpha", alpha.RevisionID); err != nil {
		t.Fatal(err)
	}
	results, err = database.SearchMemory("owner", "alpha", "retry", 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("deleted result = %+v, %v", results, err)
	}
	deleted, err := database.MemoryRevision("owner", "alpha", alpha.RevisionID)
	if err != nil || deleted == nil || deleted.Status != MemoryDeleted || len(deleted.Content) != 0 {
		t.Fatalf("deleted record = %+v, %v", deleted, err)
	}
	if bytes.Contains(deleted.Content, []byte("secret")) {
		t.Fatal("deleted plaintext remained")
	}
}

func TestMemoryBundleRoundTripIsIdempotent(t *testing.T) {
	first, err := Open(t.TempDir() + "/first.db")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	record := &MemoryRecord{OwnerID: "owner", BoundaryID: "alpha", Kind: MemoryAssertion, SourceActor: "user", SourceType: "note", Content: []byte("portable claim"), SPIFEnvelope: []byte("spif"), Confidence: .8}
	if err := first.InsertMemoryRecord(record); err != nil {
		t.Fatal(err)
	}
	bundle, err := first.ExportMemoryBundle("owner", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(t.TempDir() + "/second.db")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.ImportMemoryBundle(bundle); err != nil {
		t.Fatal(err)
	}
	if err := second.ImportMemoryBundle(bundle); err != nil {
		t.Fatal("duplicate import was not idempotent:", err)
	}
}
