# Changelog

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
