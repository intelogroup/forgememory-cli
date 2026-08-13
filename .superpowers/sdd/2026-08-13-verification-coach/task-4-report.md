# Task 4 report: coaching queue, modes, and lifecycle

## Review fixes

- Deferral is non-terminal: queued items take precedence at a safe boundary, then deferred normal/strict items are reconsidered newest-first and can be accepted or dismissed.
- Queue eligibility now trusts the project-local `suspected_gap` state from `internal/skills`; duplicate confidence and evidence thresholds were removed.
- Added explicit complete repository reads for queue reconciliation and suppression, while existing public list calls retain their 100-row default bound.
- Added `Config.CoachMode` and `FORGE_COACH_MODE` load, save, and environment propagation for every supported mode.
- Added regression coverage for a single high-confidence event, unknown/resolved lifecycle actions, 101 observations, and suppression outside the public page bound.
- Added stable `id DESC` tie-breakers to observation and coaching-item newest-first queries; commit `ce84f2c`.

## Verification

- Red/green regressions confirmed deferred items were not eligible, low-confidence observations were independently rejected, and 101-record queue/suppression paths were truncated before the fixes.
- Focused: `GOCACHE=/tmp/forge-go-cache go test ./internal/coach ./internal/db ./internal/config ./internal/skills ./internal/evidence -count=1` passed.
- Focused tie-ordering: `GOCACHE=/tmp/forge-go-cache go test ./internal/coach ./internal/db -count=1` passed, including equal-timestamp safe-boundary selection.
- Full: `GOCACHE=/tmp/forge-go-cache go test ./... -count=1` passed.
- `git diff --check` passed. Manual self-review against the Task 4 brief found no remaining scoped issues; subagents were not used as required.

## Scope

- Changed only coaching, coaching repositories, configuration, their tests, and this report.
- Preserved and excluded unrelated `graphify-out/` and `.scratch/` worktree changes.
