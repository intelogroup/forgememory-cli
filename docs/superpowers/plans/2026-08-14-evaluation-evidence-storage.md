# Evaluation Evidence Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make ForgeMemo a reliable evaluation evidence store by keeping structured trace metadata in SQLite and large raw evidence in content-addressed artifact files.

**Architecture:** SQLite remains the canonical index for evaluation tasks, trace events, evaluations, and artifact metadata. Large transcripts, diffs, test reports, and logs are stored under the Forge data directory using SHA-256 content-addressed paths; SQLite stores their ownership, type, hash, size, and media type. LanceDB is intentionally deferred to a later optional semantic-index phase.

**Tech Stack:** Go, SQLite via modernc.org/sqlite, SHA-256, atomic filesystem writes, existing dashboard HTTP API.

**Spec:** Product requirements from the user conversation: complete evaluation evidence, SQLite source of truth, filesystem artifacts for large blobs, optional LanceDB only for semantic retrieval.

## Global Constraints

- Preserve existing dirty and untracked user files.
- Keep secrets redacted before persistent storage.
- Do not add a LanceDB dependency in this phase.
- Do not create summary reports; communicate results in chat.
- Artifact writes must be retry-safe and content-addressed.

---

### Task 1: Add the artifact metadata schema and DB accessors

**Files:**
- Modify: `internal/db/db.go`
- Create: `internal/db/artifacts.go`
- Test: `internal/db/artifacts_test.go`

**Interfaces:**
- `db.EvaluationArtifact` describes one stored artifact.
- `DB.InsertEvaluationArtifact(*EvaluationArtifact) error` inserts or idempotently replaces metadata by artifact ID.
- `DB.EvaluationArtifacts(traceID, taskID string, limit int) ([]EvaluationArtifact, error)` lists artifacts scoped by trace or task.

- [ ] Add an `evaluation_artifacts` table with trace/task ownership, kind, media type, path, SHA-256, byte size, creation time, and metadata JSON.
- [ ] Add indexes for trace and task lookup.
- [ ] Add round-trip tests and verify duplicate artifact IDs remain idempotent.
- [ ] Run `go test ./internal/db`.

### Task 2: Add a content-addressed artifact store

**Files:**
- Create: `internal/artifacts/store.go`
- Test: `internal/artifacts/store_test.go`

**Interfaces:**
- `artifacts.Store{Root string}`.
- `Store.Put(traceID, taskID, kind, mediaType string, content []byte, metadata string) (db.EvaluationArtifact, error)`.
- `Store.Open(artifact db.EvaluationArtifact) (io.ReadCloser, error)`.

- [ ] Sanitize textual evidence with the existing secret scrubber.
- [ ] Hash stored bytes with SHA-256 and use `<root>/sha256/<first-two>/<hash>`.
- [ ] Write through a private temporary file and atomic rename.
- [ ] Return the same artifact identity for repeated writes of the same content.
- [ ] Test deduplication, persisted content, metadata, and secret redaction.
- [ ] Run `go test ./internal/artifacts`.

### Task 3: Expose artifacts through the local dashboard API

**Files:**
- Modify: `internal/dashboard/dashboard.go`
- Modify: `internal/dashboard/dashboard_test.go`

**Interfaces:**
- `POST /api/artifacts` accepts JSON metadata plus base64 content and persists an artifact.
- `GET /api/artifacts?trace_id=...&task_id=...` returns artifact metadata only.
- `GET /api/artifacts/<id>` streams the stored bytes.

- [ ] Wire the dashboard server to an artifact store rooted beside the database.
- [ ] Validate required trace ownership and artifact kind.
- [ ] Keep list responses metadata-only.
- [ ] Add endpoint tests for upload, list, download, and missing artifacts.
- [ ] Run `go test ./internal/dashboard`.

### Task 4: Document the evaluation evidence contract

**Files:**
- Modify: `README.md`
- Modify: `cmd/main.go`

- [ ] Document SQLite metadata versus filesystem artifact storage.
- [ ] Document artifact kinds: `transcript`, `diff`, `test-report`, `command-log`, and `other`.
- [ ] Document the dashboard API examples.
- [ ] Document that LanceDB is not canonical and is deferred to a future semantic-search integration.
- [ ] Run focused tests and inspect `git diff`.

## Verification

- `go test ./internal/db ./internal/artifacts ./internal/dashboard`
- `go test -p 1 ./...` if the environment has sufficient process capacity.
- Confirm unrelated existing modifications remain unchanged with `git status --short`.

