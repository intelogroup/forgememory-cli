# Task 4 report: coaching queue, modes, and lifecycle

## Delivered

- Added deterministic `internal/coach` queueing for project-local repeated, high-confidence `suspected_gap` evidence.
- Added parsing for `off`, `observe`, `quiet`, `normal`, and `strict`; an empty mode defaults to observation-only.
- Added a provider-free lesson template with the required Socratic question and narrow-test next action.
- Made queueing observation-deduplicated and safe-boundary selection read-only, project-scoped, and limited to one normal/strict item.
- Added accept, defer, and dismiss lifecycle actions. Acceptance delegates the state transition to `skills.Evaluate`; dismissal retains its reason, including project/skill `never_show_again` suppression.

## Test coverage

- Mode parsing and invalid-mode rejection.
- Low-confidence suppression, repeated-evidence gating, project scope, and idempotent queueing.
- All five modes, including the empty-mode observation-only default.
- Default lesson wording and delivery mode.
- One non-mutating safe-boundary suggestion.
- Accept-to-learning, defer, dismissal reason, and never-show suppression.

## Verification

- Red phase: `GOCACHE=/tmp/forge-go-cache go test ./internal/coach -count=1` failed because the package APIs did not yet exist.
- Regression red phase: the empty-mode case failed until `QueueEligible` normalized parsed modes.
- Focused verification: `GOCACHE=/tmp/forge-go-cache go test ./internal/coach ./internal/skills ./internal/evidence -count=1` passed.
- Full verification: `GOCACHE=/tmp/forge-go-cache go test ./... -count=1` passed with local listener/socket permission enabled.
- `git diff --check` passed.
- Direct self-review against the Task 4 brief found no remaining issues. The parallel-subagent code-review skill was intentionally not used because the task explicitly prohibited subagents.

## Scope

- Changed only `internal/coach` and this report.
- Preserved and excluded unrelated `graphify-out/` and `.scratch/` worktree changes.
