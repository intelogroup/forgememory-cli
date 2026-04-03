# Changelog

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
