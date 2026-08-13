## Task 5 report — bounded backfill and background processing

### Delivered

- Added `coach.Backfill(ctx, database, projectID, limit)` with a default limit of 20 and an upper bound of 100 sessions.
  - Selects only commit-linked sessions and sessions with repeated failures.
  - Requires a completed, single-project session before deterministic detection.
  - Uses the normal detector, evidence store, skill evaluator, and coaching queue with `Live=false`.
  - Labels saved evidence with existing `backfill` provenance and reports selected, processed, skipped, created, and queued work.
- Added `coach.ProcessCompletedSession(ctx, database, sessionID)`.
  - Uses the same deterministic pipeline with `Live=true`.
  - Applies each skill-state transition and its compact processing marker in one SQLite transaction. A failed marker write rolls back the state transition, so a retry applies the observation exactly once.
- Backfill now reads only a limit-sized, newest-first set of unresolved repeated failure signatures before selection. A partial SQLite index supports that bounded query without sorting the full project failure history.
- Preserved `FailureSignaturesByProject` as the unfiltered history API used by profile, stats, and knowledge-gap callers; unresolved/repeated filtering remains limited to `FailureSignaturesByProjectLimited`.
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
- Interrupted marker persistence rolls back state and a later retry applies it once.
- Default (20) and maximum (100) backfill limits, including retry-safe replay across both bounds.
- Limited failure-history selection excludes one-off and resolved signatures.
- Legacy failure-history reads continue to return one-off and resolved signatures while limited reads exclude them.
- Provider absence and coaching persistence failures do not prevent later hook event capture.

### Verification

- `go test ./internal/coach -run 'Backfill|ProcessCompletedSession' -count=1` — PASS
- `go test ./cmd -run 'DaemonProcessesCompletedVerification' -count=1` — PASS
- `go test ./internal/db -run 'FailureSignaturesByProjectLimited' -count=1` — PASS
- `go test ./internal/coach -count=1` — PASS
- `go test ./internal/db -count=1` — PASS
- `go test ./cmd -run 'DaemonProcessesCompletedVerification|Coach' -count=1` — PASS
- `go test ./...` — PASS
- `git diff --check` — PASS

### Scope review

Only `internal/coach`, `internal/db`, focused daemon tests, and this required task report were changed for these review findings. Existing graphify and unrelated user work were not modified.
