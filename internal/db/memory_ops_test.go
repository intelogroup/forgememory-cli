package db

import "testing"

func TestMemoryOps(t *testing.T) {
	database := openPrinciplesDB(t)

	// Rows written out of chronological order; MemoryOps orders by ts DESC.
	for _, op := range []MemoryOp{
		{ID: "op-1", TS: "2026-01-01T00:00:00Z", Op: "ADD", TargetID: "p1", Reason: "first"},
		{ID: "op-2", TS: "2026-01-02T00:00:00Z", Op: "UPDATE", TargetID: "p1", Reason: "second", Model: "m"},
		{ID: "op-3", TS: "2026-01-03T00:00:00Z", Op: "REVIVE", TargetID: "p2", Reason: "third"},
	} {
		if err := database.InsertMemoryOp(op.ID, op.TS, op.Op, op.TargetID, op.BeforeJSON, op.AfterJSON, op.Reason, op.Model, "batch-1"); err != nil {
			t.Fatalf("InsertMemoryOp(%s): %v", op.ID, err)
		}
	}

	all, err := database.MemoryOps("", 10)
	if err != nil {
		t.Fatalf("MemoryOps: %v", err)
	}
	if len(all) != 3 || all[0].ID != "op-3" {
		t.Fatalf("all ops = %+v, want newest first", all)
	}

	one, err := database.MemoryOps("p1", 10)
	if err != nil {
		t.Fatalf("MemoryOps(p1): %v", err)
	}
	if len(one) != 2 || one[0].ID != "op-2" || one[1].ID != "op-1" {
		t.Fatalf("p1 ops = %+v", one)
	}

	limited, err := database.MemoryOps("", 1)
	if err != nil {
		t.Fatalf("MemoryOps(limit): %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "op-3" {
		t.Fatalf("limited ops = %+v", limited)
	}

	missing, err := database.MemoryOps("nope", 10)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing target: ops=%v err=%v, want empty", missing, err)
	}
}
