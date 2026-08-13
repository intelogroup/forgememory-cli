## Task 5 report — bounded backfill and background processing

### Delivered

- Added `coach.Backfill(ctx, database, projectID, limit)` with a default limit of 20 and an upper bound of 100 sessions.
  - Selects only commit-linked sessions and sessions with repeated failures.
  - Requires a completed, single-project session before deterministic detection.
  - Uses the normal detector, evidence store, skill evaluator, and coaching queue with `Live=false`.
  - Labels saved evidence with existing `backfill` provenance and reports selected, processed, skipped, created, and queued work.
- Added `coach.ProcessCompletedSession(ctx, database, sessionID)`.
  - Uses the same deterministic pipeline with `Live=true`.
  - Records a compact processing marker on each observation after its skill-state update, preventing normal retries from duplicating state counts, observations, or coaching items.
- Integrated completed-session processing into the daemon's existing distillation batch as deferred post-batch work.
  - It still runs when distillation is provider-less or backoff-limited.
  - Processing failures are logged and isolated; hook event capture remains independent.
  - Daemon configuration now forwards `CoachMode` to the background processor.

### TDD evidence

- Backfill/process tests were written first and initially failed because `Backfill` and `ProcessCompletedSession` did not exist.
- Daemon integration test then failed before integration: a completed session produced no observation after provider-less distillation.
- Both sets passed after the minimal implementation.

### Coverage added

- Bounded high-signal selection and exclusion of ordinary sessions.
- Historical provenance, idempotent reruns, and retry-stable skill state.
- Skipped ambiguous/document-only historical sessions.
- Live completed-session provenance and retry safety.
- Provider absence does not prevent deterministic completed-session processing; later hook events still persist.

### Verification

- `go test ./internal/coach -run 'Backfill|ProcessCompletedSession' -count=1` — PASS
- `go test ./cmd -run 'DaemonProcessesCompletedVerification' -count=1` — PASS
- `go test ./internal/coach ./cmd -run 'Coach|Backfill|Daemon' -count=1` — PASS
- `go test ./internal/coach ./cmd -count=1` — PASS
- `go vet ./internal/coach ./cmd` — PASS
- `go test ./...` — PASS
- `git diff --check` — PASS

### Scope review

Only `internal/coach`, `cmd/daemon.go`, focused tests, and this required task report were changed. Existing graphify and unrelated user work were not modified.
