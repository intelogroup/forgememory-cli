# CLAUDE.md

Go CLI + payment server for Forgememo. Captures agent tool usage, distills insights, injects context.

**Repo:** `intelogroup/forgememory-cli` — release via `git tag v*`, CI builds all platforms + publishes npm
**Web app:** `forgememory-app/` is a separate repo (`intelogroup/forgememory-app`) with its own git remote

## Structure

| Path | Purpose |
|------|---------|
| `cmd/cli.go` | Main CLI — `forge init/start/stop/login/config/distill/status` |
| `internal/distill/` | Distillation engine (supports Forgememo, Anthropic, OpenAI, Groq, NVIDIA, Ollama) |
| `internal/config/` | Config read/write (`~/.forge/config`) |
| `internal/db/` | SQLite DB for runs/memory |
| `internal/service/` | Daemon lifecycle (launchd/systemd) |
| `internal/mcp/` | MCP server for agent integration |
| `internal/scanner/` | Agent detection (Claude Code, Codex, Gemini) |
| `payment/main.go` | Go HTTP server — auth, billing, inference proxy |
| `npm/` | npm wrapper package (`forgememo-cli`) |
| `forgememory-app/` | Next.js web dashboard (separate git repo) |

## Local dev

```bash
go build ./cmd/        # build CLI
go test ./...          # run tests

# Payment server (port 8000) — needs Supabase creds
cd payment
SUPABASE_URL=... SUPABASE_ANON_KEY=... go run main.go
```

## CLI login flow

`forge login` opens a browser to `/cli-auth` on the configured server, starts a local callback server on a random port, and waits for the auth token via redirect. With `--purchase`, a second callback server handles the payment confirmation after Stripe checkout.

Key: `FORGE_BASE_URL` env overrides the default `https://forgememo-server.onrender.com`.

## Payment server routes

| Route | Purpose |
|-------|---------|
| `POST /webapp-auth/send-link` | Create/lookup user, email magic link (returns `magic_link` in dev when no `RESEND_API_KEY`) |
| `GET /health` | Health check |
| `POST /v1/checkout` | Create Stripe checkout session (returns fake URL in dev when no `STRIPE_SECRET_KEY`) |
| `POST /api/deduct` | Deduct credits per run |
| `GET /v1/stats` | Balance + run count |

## Release

```bash
# Bump VERSION + npm/package.json, then:
git tag v0.4.x && git push origin main v0.4.x
# → GitHub Actions builds binaries, publishes npm
```

## Testing CLI install (Lima VM)

```bash
# Build for Linux ARM64 (ubuntu-24 VM is aarch64)
GOOS=linux GOARCH=arm64 go build -o /tmp/forge-linux-arm64 ./cmd/
limactl copy /tmp/forge-linux-arm64 ubuntu-24:/tmp/forge
limactl shell ubuntu-24 -- env HOME=/tmp/test-user /tmp/forge init
```
