# Task 3 report: evidence persistence and skill transitions

## Delivered

- Added `internal/evidence.Store.Save` for auditable observation persistence.
  - Uses a deterministic UUID derived from project, session, kind, and ordered source identities for idempotent retries.
  - Persists supporting and counter source references, plus compact live/backfill provenance.
  - Stores the extractor version on the observation and does not access or persist raw provider payloads or diffs.
- Added provider-independent `internal/skills.Evaluate` and `EvidenceSummary`.
  - Named deterministic thresholds cover unobserved, suspected gap, learning, applied, reliable, and regressed transitions.
  - Preserves cumulative evidence/application counts and lowers confidence for counter-evidence.
  - Keeps project evidence local and permits global updates only from normalized transferable successes.

## Test coverage

- Auditable source and provenance persistence.
- Source-order-independent idempotency and backfill provenance.
- Weak evidence, repeated negatives, acceptance, first success, cross-session reliability, and later strong-negative regression.
- Historical-count preservation, confidence reduction, weak single-negative handling, and global-transfer gating.

## Verification

- Red phase: `go test ./internal/evidence ./internal/skills -count=1` failed because the new APIs did not yet exist.
- Focused verification: `go test ./internal/evidence ./internal/skills ./internal/db -count=1` passed.
- Full verification: `go test ./... -count=1` passed.
- Self-review: requirements and final diff checked; no findings. Unrelated `graphify-out/` and `.scratch/` worktree changes were not modified or staged.
