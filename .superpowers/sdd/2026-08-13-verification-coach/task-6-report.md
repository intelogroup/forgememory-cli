# Task 6 report: coach CLI

Implemented the public `forge coach` subcommands while preserving the existing flag-based detector and item-ID workflow:

- Read commands: `status`, `list`, `explain <observation-id>`, and `review`; each supports JSON-only output.
- Lifecycle commands: `accept`, `defer`, and `dismiss <observation-id> --reason <category>`.
- `explain` reports confidence, skill state, supporting evidence, counter-evidence, and whether more evidence is needed.
- Observation-ID lifecycle actions resolve through the coach service, which owns transitions and reason-category validation.
- Added command-level coverage for empty JSON schemas, evidence detail, lifecycle errors, invalid reasons, review, and help text.

Verification completed:

- `go test ./cmd -run '^TestCoach' -count=1`
- `go test ./internal/coach ./internal/db -count=1`
- `go test ./... -count=1`
