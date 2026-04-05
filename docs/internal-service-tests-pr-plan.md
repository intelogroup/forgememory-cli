# Internal Service Test PR Plan

## Objective
- Raise `internal/service` from `0.0%` to meaningful coverage with deterministic tests that run on any CI OS.
- Reduce operational risk in service install/start/stop/uninstall flows (launchd, systemd, Windows service/task fallback).

## Current Risk Snapshot
- `go test ./internal/service -cover -count=1` reports `coverage: 0.0% of statements`.
- Service lifecycle code executes OS commands directly (`launchctl`, `systemctl`, `sc`, `schtasks`) and writes system files.
- No test seam currently exists for `runtime.GOOS` or command execution, so branch logic is untested.

## PR Scope (Focused)
1. Add test seams in `internal/service/service.go`:
- OS selector seam (instead of hard dependency on `runtime.GOOS` in methods).
- Command runner seam (instead of direct `exec.Command(...).Run()/Output()` everywhere).
- File/dir op seams for write/stat/remove on service definition files.
2. Add `internal/service/service_test.go` with table-driven unit tests for:
- `Install`, `Uninstall`, `Start`, `Stop`
- `IsServiceInstalled`, `ServiceStatus`
- `installWindowsService` fallback behavior to scheduled task
3. Add one CLI-level smoke test set for service command handlers in `cmd` package:
- Success and error-path behavior for `runServiceInstall`, `runServiceUninstall`, `runServiceStart`, `runServiceStop`.
- Keep these minimal; heavy behavior remains in `internal/service` tests.
4. Wire CI coverage gate for this package only (initial threshold recommendation: `>= 65%` statements in `internal/service` for this PR).

## Non-Goals
- End-to-end validation with real systemd/launchd/Windows service manager.
- Cross-machine privileged integration tests.
- Broad command refactors outside service lifecycle files.

## Test Matrix
| Area | Case | Expected |
|---|---|---|
| Constructor | `New()` happy path | returns non-empty `BinaryPath` and `HomeDir` |
| Install routing | GOOS `darwin` | calls launchd installer path |
| Install routing | GOOS `linux` | calls systemd installer path |
| Install routing | GOOS `windows` | calls Windows installer path |
| Install routing | unsupported GOOS | returns `unsupported OS` error |
| Uninstall routing | per-OS routing | dispatches to correct uninstall implementation |
| Start routing | darwin/linux/windows | invokes expected command signature |
| Start routing | unsupported GOOS | returns `unsupported OS` error |
| Stop routing | darwin/linux/windows | invokes expected command signature |
| Stop routing | unsupported GOOS | returns `unsupported OS` error |
| launchd install | mkdir/write success | plist path created with expected content keys |
| launchd uninstall | unload + remove | remove attempted; unload invoked |
| systemd install | mkdir/write/reload success | unit file path and `systemctl --user daemon-reload` invoked |
| systemd uninstall | stop + disable + remove | all expected operations attempted |
| Windows install | `sc create` success | returns nil and no fallback call |
| Windows install fallback | `sc create` failure | falls back to `schtasks /create` |
| Windows uninstall | stop + delete | `sc stop forge`, then `sc delete forge` invoked |
| Installed check | darwin path exists | `IsServiceInstalled() == true` |
| Installed check | linux path missing | `IsServiceInstalled() == false` |
| Installed check | windows `sc query` output contains service name | returns true |
| Status | not installed | returns `not installed` |
| Status darwin | launchctl output contains label | returns `installed` |
| Status linux | systemctl `is-active` outputs `active` | returns trimmed output |
| Status windows | `sc query` contains `RUNNING` | returns `running` |
| Status windows non-running | query without `RUNNING` | returns `installed` |

## Suggested Acceptance Criteria
- `go test ./internal/service -cover -count=1` passes with target threshold reached.
- `go test ./cmd -run "Service" -count=1` passes for new service command smoke tests.
- No behavior change in production command strings or service file paths.
- Coverage gate added so future service changes cannot silently regress to 0%.

## Implementation Notes
- Keep seams package-private vars/functions so external API remains unchanged.
- Preserve current command strings exactly to avoid runtime behavior drift.
- Prefer table-driven tests with explicit `name`, `goos`, `command`, `wantErr`, `wantStatus`.
