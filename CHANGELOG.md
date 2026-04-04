# Changelog

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
