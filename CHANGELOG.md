# Changelog

## [0.6.6] - 2026-07-16

### Added
- `forge inject-check --pre-push` — git pre-push hook that blocks pushes touching files with high-impact learned principles
- `forge context` auto-relevant section — silently detects changed files and shows matching principles and their full narrative
- `forge context --relevant <query>` now shows full narrative (no 120-char truncation)

### Fixed
- `detectRecentFiles` and `getPushFiles` git porcelain parsing: TrimSpace before `line[3:]` ate the first character of filenames
- `getPushFiles` now also checks untracked files via `git ls-files --others`

## [0.6.4] - 2026-07-14

### Changed
- Context is now injected only once, on the first prompt of a session, instead of on every `UserPromptSubmit` — background prompt retrieval still runs so distillation keeps working, but repeated-failure/docs-hint/prompt-match text no longer gets re-injected on later messages.
- Enforced a 300-event minimum for `distill_batch_size`: `forge config --distill-batch-size` and `forge distill --batch-size` now reject values between 1-299 (0 still means "use default"), and the loaded config floor-clamps any stored/env value below 300 up to 300.

## [0.6.3] - 2026-07-10

### Fixed
- Fixed distillation backlog bottleneck: increased session batch limit to 20 per cycle and added burst-draining to continually process pending sessions without waiting for the next ticker tick.
- Mitigated daemon flapping by implementing a 3-strike parent PID monitor check, letting brief parent restarts or flaps occur without killing the daemon.
- Prevented 30-min backoffs on provider URL/model configuration errors by mapping HTTP 404 to `ErrProviderUnreachable` instead of `ErrProviderInvalid`.
- Improved error messages for OpenAI-compatible client calls by dynamically logging the correct provider name (e.g. OpenRouter) instead of hardcoding "OpenAI".
- Added one-time mismatch warnings when a provider (e.g. `openrouter`) is configured but its base URL is missing the provider's domain.
- Reduced `distillLockTTL` to 10 minutes to auto-heal stale lock states faster.
- Fixed startup lock races by returning the open lock file descriptor directly and adding a size/age guard to `isPIDLockStale`.

## [0.6.2] - 2026-07-02

### Changed
- Shifted context injection from broad preview mode to surgical injection that only triggers on repeated failure alerts (second occurrence onward) to minimize token noise and context bloat.

## [0.6.1] - 2026-07-01

### Fixed
- Fixed silent distillation no-op (#34): if the entire undistilled backlog had `session_id='unknown'` (orphan hook events captured without SessionStart context), `forge distill --all --wait` returned in 15ms reporting `success` with zero LLM calls and zero events drained — same class of silent-success-while-frozen bug as #33.
- New `UndistilledEventsIncludingUnknown` / `DistillBatchIncludingUnknown` paths let the `--all` drain flush the orphan backlog so it no longer stalls forever behind permanently-excluded events. The one-shot `forge distill` and `daemon` `distillLoop` keep the v0.5.13 exclude-by-default behavior.
- `runDistillDrain` now surfaces a true stall as a distinct failure: after two consecutive no-op batches with zero progress AND zero principles, it prints `Drain stalled. ...` to stderr, writes a failure record to `distillation_health`, and exits non-zero instead of pretending `Drain complete`.

## [0.6.0] - 2026-07-01

### Fixed
- Fixed silent scheduler wedge (#33): the daemon's periodic distillation cycle could wedge forever when the distill lock was held by a leaked/stale external `forge distill` process (e.g. ephemeral npx temp-path installations). Health stayed `SUCCESS` while the undistilled backlog grew unbounded and clean restarts failed to recover.
- Distill lock now reclaims itself via a 30-minute TTL: any lock older than `distillLockTTL` is treated as stale even when its recorded PID is technically alive (protects against PID reuse and leaked zombies), so a future scheduler cycle auto-clears it.
- Daemon startup now unconditionally clears any orphaned `forge.distill.lock` — manual distill never spans a daemon restart, so a lingering lock at startup is stale by definition.
- After 3 consecutive skipped scheduled cycles (locked out), the scheduler now writes a `WEDGED` failure record to the distillation health table and logs a clear `Distillation WEDGED: ...` line, so `forge status` / `forge health` surface the failure instead of falsely reporting `SUCCESS`.

## [0.5.16] - 2026-07-01

### Fixed
- Fixed config resolution precedence in `LoadConfig()` to prioritize `~/.forge/config` file settings over shell environment variables (`FORGE_PROVIDER`, `FORGE_API_KEY`, etc.), preventing stale shell variables from causing configuration bleeding or false positives/failures in `forge doctor` and `forge distill`.

## [0.5.15] - 2026-07-01

### Fixed
- Isolated background daemon process from inherited terminal environment variables (`FORGE_PROVIDER`, `FORGE_API_KEY`, etc.) when spawned via `forge start`.
- Prevented unconfigured Exa API key errors from looping indefinitely by skipping neural search planning when search keys are missing.

## [0.5.14] - 2026-07-01

### Fixed
- Fixed distillation queue fragmentation caused by chronological event interleaving. Forge now query-groups by oldest undistilled session ID first, ensuring full session context is preserved during distillation.

## [0.5.13] - 2026-07-01

### Fixed
- Fixed distillation queue clogging by unconditionally marking evaluated events as distilled.
- Fixed config provider changes bleeding settings by resetting provider-specific values on provider update.
- Fixed stale errors in `forge health` by clearing the error state upon successful distillation.
- Improved warning banner formatting by namespacing the background distillation failure notice to prevent AI agent prompt-injection confusion.
- Excluded legacy `session_id = 'unknown'` events from CLI distillation queries.

### Added
- Documented configuration precedence, background caching rules, and distillation thresholds.

## [0.5.12] - 2026-07-01

### Added
- Added `openrouter` as a first-class supported provider with default model `google/gemini-2.5-flash` and default base URL `https://openrouter.ai/api/v1`.
- Added automatic verification and bypass of global Gemini constraints for OpenRouter configurations.

## [0.5.11] - 2026-07-01

### Added
- Implemented native Go distillation in the daemon process, removing dependency on the TS/Mastra distillation agent.
- Added interactive config diffing and TTY-aware prompting to prevent silent configuration rewrites.
- Scoped cross-project fallback principles and added failed-distillation health warnings to `<forge-context>`.

### Fixed
- Silenced unconfigured neural search logs inside the daemon loop.

## [0.5.10] - 2026-07-01

### Fixed
- Fixed setup deduplication for Claude Code settings by matching `forgememo` in stale hook path checks and clearing the `forgememo` MCP server.

## [0.5.9] - 2026-06-30

### Removed
- Removed the `compact-check` rewake hook for Claude Code.

## [0.5.8] - 2026-06-21

### Added
- Added relevance-based filtering using FTS5 for query hints (`query_hint`) and Jaccard similarity for active/modified files (`active_files`) in the principle injection system.
- Exposed `query_hint` and `active_files` as optional parameters in the `inject_principles` MCP tool schema and environment variables `FORGE_INJECT_QUERY_HINT` and `FORGE_INJECT_ACTIVE_FILES`.

### Fixed
- Fixed SQLite database write-lock contention on hook execution by implementing read-only database connections (`db.OpenReadOnly`) and configuring a fast-failing busy timeout.
- Fixed hardcoded MCP version to dynamically reflect the build version.

## [0.5.7] - 2026-06-20

### Added
- Added self-healing MCP daemon recovery. The MCP server now automatically detects if the background daemon is not running and starts it dynamically during tool execution.
- Added automatic registration in `install.sh` and `install.ps1` scripts by invoking `forgememo init` automatically.

## [0.5.6] - 2026-06-20

### Fixed
- Fixed Windows compatibility issues including home directory override resolution during test isolation, path normalization, `.exe` extension handling, and increased daemon startup timeouts on slow runners.

## [0.5.5] - 2026-06-17

### Fixed
- Resolved Mastra compilation errors and integrated custom provider logic (`forgememo` API endpoint, `antigravity` CLI, and `codex` CLI) in the distillation agent.

## [0.5.4] - 2026-06-17

### Added
- Exposed `reconfigure_provider` capability contract over MCP.
- Added dynamic configuration reloading to the background distillation daemon.
- Enhanced loop safety bounds and parameter limits.

## [0.5.3] - 2026-05-29

### Added
- **NVIDIA Build API support.** NVIDIA is now a first-class provider. Configure with `forge config --provider nvidia --api-key nvapi-...`. Includes native defaults for NVIDIA NIM endpoints and model suggestions (Llama 3.3 70B, Llama 3.1 405B).
- **Interactive NVIDIA model selection.** The `forge config` interactive mode now provides recommendations for NVIDIA-hosted models.

### Fixed
- **OpenAI 429 Rate Limit mitigation.** Added support for alternative high-throughput providers (NVIDIA) to resolve persistent distillation failures during peak usage.

## [0.4.38] - 2026-05-07

### Fixed (OpenCode compatibility round)
- **OpenCode plugin no longer requires the `$` shell helper.** The auto-generated
  `~/.config/opencode/plugins/forge.js` previously destructured `{ $ }` from the
  plugin context and used tagged template literals — OpenCode does not expose
  `$`, so every event raised `$ is not a function`. The plugin now spawns the
  forge hook binary via `node:child_process.spawn`, which is universally
  available and avoids shell-quoting risk on the JSON payload.
- **`--base-url` no longer double-appends `/v1`.** Setting `--base-url
  http://host:11434/v1` produced `/v1/v1/chat/completions` → 404. A new
  `normalizeOpenAIBase` helper strips trailing `/` and `/v1` for OpenAI, Groq,
  and Anthropic paths.
- **MCP server stops logging `-32601 Method not found` for `resources/list` and
  `prompts/list` probes.** Returns empty `{resources: []}` /
  `{resourceTemplates: []}` / `{prompts: []}` instead. Genuinely unknown
  methods still produce `-32601`.
- **`forge start` no longer rewrites agent skill files on every boot.** The
  stale-hook detection now compares realpath + SHA256, not raw strings, so
  npm-wrapper paths, symlinks, and same-version installs at different paths
  are recognized as equivalent. A re-write only happens when the registered
  binary is missing or its content actually differs.

### Added
- **Auto-validation on config changes.** `forge config --provider` /
  `--api-key` / `--model` / `--base-url` now runs `distill.ValidateConfig`
  by default and refuses to save on credential failure (config left
  unchanged). New `--no-validate` opts out for offline scripts. Prevents the
  "1-minute opaque-error treadmill" where a stale key kept firing failed
  distillations every minute with no clear feedback.
- **Exponential backoff on consecutive distillation failures.** Schedule:
  no backoff for 0–1 failures, then 1m, 2m, 4m, 8m, 16m, capped at 30m.
  After 3+ consecutive failures the daemon also annotates the recorded
  error with `(N consecutive failures, next retry in Xm)` and logs at
  CRITICAL severity, surfacing through `forge status` and the `get_alerts`
  MCP tool.
- **`forge config` warns about env-vs-config drift at save time.**
  Previously the warning only fired at `forge start`, leaving stale shell
  exports to silently shadow the new value when the daemon next reloaded.

## [0.4.23] - 2026-04-07

### Fixed
- Fixed a TOCTOU race in `acquireDistillLock` where a goroutine could read an
  empty lock file (PID not yet written) as stale, remove it, and acquire a second
  lock — causing concurrent distillation. Fix uses a process-level `sync.Mutex`
  fast path (`TryLock`) to reject same-process concurrency before touching the
  filesystem, plus hardened `isDistillLockStale` to distinguish empty in-progress
  files (size=0, mtime < 1s) from garbage content.

### Added
- `forge agent-guide` — new command that prints a copy-pasteable CLAUDE.md /
  system prompt block with correct MCP tool call triggers.
- `forge help mcp` — expanded from a single-line summary to full per-tool docs
  with `When`, `Returns`, and `Params` for all 9 MCP tools.
- `--help` now includes an **Agent setup** section with a 5-step onboarding guide.

### Changed
- All 9 MCP tool descriptions rewritten to lead with a trigger condition
  ("Call at session start…", "Call when something is broken…") so agents know
  *when* to invoke each tool, not just what it returns.

## [0.4.14] - 2026-04-05

### Added
- Added persistent distillation health tracking in SQLite (`distillation_health`) with last run/success/failure timestamps, last error, next schedule, and failure counters.
- Added `forge health` command for direct CLI visibility into distillation status, failure streaks, and backlog alerts.
- Added MCP health tools: `get_distillation_health` and `get_alerts` for agent-readable failure/backlog monitoring.

### Changed
- Extended `forge status --json` to include a `distillation` object with run health metadata.
- Added `forge status --detailed` to print status plus health summary in one command.

### Fixed
- Distillation failures are now observable without manual log tailing; daemon loop records success/failure state every cycle.

## [0.4.13] - 2026-04-05

### Added
- Added configurable distillation controls: `forge config --timeout`, `--retries`, and `--interval`, plus interactive setup and provider/model selection prompts.
- Added machine-readable output flags for automation: `forge status --json`, `forge search ... --json`, and `forge config --show --json`.
- Added `forge help mcp` command to list MCP tools and intended usage.

### Changed
- Set Anthropic default model to `claude-haiku-4-5-20251001`.
- Promoted `forgememo` as the default provider and added `forge` alias compatibility for existing config.
- Expanded status output to include provider, model, database path, and relative last-distilled time.

### Fixed
- Added Ollama retry/backoff handling and clearer distillation diagnostics with actionable remediation steps.
- Updated Windows CI integration checks to parse `status --json` for stable assertions.
- Documented provider defaults and `~/.forge/config` format in README.

## [0.4.12] - 2026-04-05

### Fixed
- Updated release installers to install `forgememo` directly without npm as the primary command and keep `forge` as a compatibility alias.
- Updated Quick Install and usage docs to standardize on no-npm installation and `forgememo` command examples.

## [0.4.11] - 2026-04-05

### Added
- Added comprehensive `internal/service` unit tests covering launchd/systemd/Windows routing, status checks, and Windows scheduled-task fallback behavior.
- Added focused CLI service command tests for install/uninstall/start/stop success and exit-on-error paths.

### Fixed
- Added a CI coverage gate enforcing `internal/service` coverage at or above 65% to prevent regression to untested service lifecycle code.
- Aligned repo and npm package versioning to `0.4.11` so release artifacts stay consistent.

## [0.4.10] - 2026-04-04

### Fixed
- Switched GitHub Actions workflows to Node 24-capable action versions, replaced the release action with the GitHub CLI, and aligned the release workflow toolchain with `go.mod` so tag releases run with the required Go version.

## [0.4.8] - 2026-04-04

### Fixed
- Recut the npm release after the `v0.4.7` tag was created from the pre-bump commit, ensuring the published package version matches the Git tag.

## [0.4.7] - 2026-04-04

### Fixed
- Forge config path resolution now honors `HOME` before falling back to the user profile, which keeps CI and temp-home test runs isolated on Windows and other platforms.

## [0.4.6] - 2026-04-04

### Added
- Added `make downloads-report` plus `scripts/download-report.sh` to compare npm download totals with GitHub release archive downloads by version and platform.

### Fixed
- Hardened daemon startup and lifecycle handling, including Linux CI preflight coverage and clearer startup recovery paths.
- Improved Windows path handling and refused-connection recovery so lifecycle commands and distillation behave more reliably across platforms.

## [0.4.5] - 2026-04-03

### Fixed
- Codex installs now register `PostToolUse` hooks and include explicit verification and repair commands so setup guidance uses exact commands instead of guessing installation state.
- Startup recall now persists `UserPromptSubmit`, scopes recall to the current project, and injects a compact summary built from recent project summaries and principles.
- Project-scoped recall remains compatible with older records saved under absolute-path project IDs.

## [0.4.4] - 2026-04-03

### Fixed
- Windows npm launcher now runs `forgememo start` in a detached child so the daemon stays alive after successful startup instead of exiting immediately through the wrapper process tree.

## [0.3.0] - 2025-04-03

### Added
- **Credit system** - New `forge login` command for paid distillation
- **Payment service** - Stripe + Supabase integration for credits
- **Config command** - `forge config` to configure inference providers
- **Provider priority** - Forge credits > OpenAI/Anthropic > Ollama fallback
- **Auto-open checkout** - Browser opens Stripe automatically
- **Signup flow** - `forge login --signup` opens registration page

### Changed
- Default provider is now Forge credits (if logged in) → Ollama fallback
- Pricing: $5 for 100 credits, 5 free credits for new users

### Fixed
- FTS5 indexing verified working
- Stress tests verified passing
- All features from roadmap implemented
