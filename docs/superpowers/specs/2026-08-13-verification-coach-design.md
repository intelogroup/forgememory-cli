# ForgeMemo Verification Coach Design

## Status

Approved design for the first vertical slice of ForgeMemo's long-term
profiling, knowledge-gap detection, and teaching system.

## Goal

Add a local-first, auditable verification coach that learns from existing
ForgeMemo events, commits, changed files, and failures. It should identify
repeated pre-ship verification risks, teach one small reasoning step, and
measure whether the developer applies the lesson independently.

The first milestone covers one project while allowing normalized verification
evidence to transfer across projects. It does not attempt to build the full
future skill taxonomy, personality model, or general-purpose event-sourcing
platform.

## Product principles

- A single question or incident never establishes a knowledge gap.
- ForgeMemo reports evidence-backed hypotheses, not judgments about
  intelligence, motivation, personality, or mental health.
- The developer owns the profile and can inspect, challenge, dismiss, or defer
  coaching conclusions.
- Hooks remain fast and non-blocking; coaching is asynchronous.
- Derived observations reference existing data rather than duplicating raw
  prompts, diffs, or traces.
- The system remains useful without an LLM provider.
- Positive application is stronger evidence of mastery than acknowledgement.

## Architecture

The implementation uses a dedicated coaching subsystem over the existing
SQLite database and event pipeline:

```text
existing events / commits / failures
        |
        v
internal/observations
        |
        v
internal/evidence
        |
        v
internal/skills
        |
        v
internal/coach
     /       \
    v         v
 CLI / MCP   safe hook suggestion
```

Responsibilities:

- `internal/observations`: deterministic facts from events, commits, and
  failures.
- `internal/evidence`: observations, source references, counter-evidence,
  confidence, provenance, and lifecycle status.
- `internal/skills`: skill definitions and deterministic state transitions.
- `internal/coach`: eligible-item selection, lesson creation, queueing, and
  feedback/outcome recording.
- `internal/db`: SQLite migrations and repository methods.
- `cmd/hook.go`: only reads already-queued suggestions at safe boundaries.
- `internal/mcp`: read-only coaching APIs for the first release.

No LLM call occurs in the hook path. LLM use is limited to ambiguous
classification, concise lesson wording, and optional explanations in the
background path.

## Initial skill

```text
key: verification.pre_ship
scope: global + project
transferable: true
```

The skill describes the observable ability to select an appropriate test
boundary, verify a meaningful behavior after a change, and independently use
that practice across sessions.

Skill states:

```text
unobserved -> suspected_gap -> learning -> applied -> reliable -> regressed
```

Each state is backed by numeric confidence and evidence counters. A strong
negative observation after `reliable` moves the state to `regressed` without
deleting history.

## Data model

Five tables are added through the existing migration mechanism.

### `skill_definitions`

Stores stable skill metadata: key, name, description, transferability, and
definition version.

### `observations`

Stores one derived observation per claim:

```text
id, created_at, project_id, session_id, kind, skill_key, confidence,
severity, status, summary, extractor_version
```

Initial observation kinds:

```text
code_change_without_relevant_test
unresolved_failure_after_change
repeated_regression
verification_detected
```

### `observation_evidence`

Links an observation to existing source records:

```text
observation_id, source_type, source_id, role, excerpt
```

Roles are `supporting`, `counter`, and `outcome`. Excerpts are optional,
sanitized, and bounded.

### `skill_states`

Stores current state by skill and scope:

```text
skill_key, scope_type, scope_id, state, confidence, evidence_count,
successful_applications, failed_applications, last_observed_at, updated_at
```

The same transferable skill can have a global state and project-local states.
Project provenance remains attached to every observation.

### `coaching_items`

Stores queued and resolved interventions:

```text
id, observation_id, skill_key, project_id, status, delivery_mode, question,
next_action, lesson, created_at, surfaced_at, resolved_at, resolution
```

Statuses include `queued`, `surfaced`, `accepted`, `deferred`, `dismissed`,
`verified`, `expired`, and `failed`.

Model/provider/prompt versions are recorded whenever semantic generation is
used. Raw payloads and diffs are not copied into these tables.

## Observation algorithm

The detector is conservative and deterministic.

1. Identify meaningful code changes from linked session commits or write-type
   tool events. Exclude documentation, lockfiles, generated files, vendored
   code, and formatting-only changes where detectable.
2. Extract test commands and outcomes from shell/tool payloads. Recognize Go,
   Node, Python, Rust, Java, Ruby, .NET, and common test commands.
3. Classify test scope as targeted, project-wide, or unknown.
4. Match changes to verification using package/file heuristics first. A
   project-wide suite is broad positive evidence; unrelated tests are not.
5. Treat verification before a change as non-verifying. Tests after a change,
   including delayed tests in the same session or commit window, are eligible
   with reduced confidence.
6. Emit positive and negative observations. Do not infer a knowledge gap from
   one weak event.

Evidence weighting:

```text
repeated regression / unresolved failure       high
change without relevant test                   medium-high
successful targeted verification               medium
successful broad verification                  medium
session ended without tests                    low
single isolated event                          insufficient
```

Counter-evidence lowers confidence rather than being discarded. Historical
backfill is marked separately from live evidence.

## Coaching loop

When repeated evidence moves `verification.pre_ship` to `suspected_gap`, the
coach queues one concise intervention. At a safe prompt/session boundary it
may surface:

> This change modified behavior, but no relevant verification was detected.
> What behavior should the test prove?

The user can request help identifying the invariant, see a test outline, say
they know what to test, defer, or dismiss. The coach does not write tests
automatically in the first milestone.

Successful real-work application is the primary learning signal. Optional
exercises and explicit feedback are supporting signals. Repeated success in
separate sessions is required for `reliable`.

Ambiguous or contradictory evidence below the automatic-coaching threshold is
stored for review as “needs more evidence,” not surfaced as a lesson.

## CLI and MCP

Initial CLI:

```bash
forge coach status
forge coach list
forge coach explain <observation-id>
forge coach accept <observation-id>
forge coach dismiss <observation-id>
forge coach review
```

Dismissal records a reason. “Never show this again” suppresses the pattern
until materially new evidence appears; it does not erase the evidence.

Initial MCP tools are read-only:

```text
get_coaching_status
list_coaching_items
explain_coaching_item
```

The hook path only reads queued items and renders them. It does not analyze
diffs, call an LLM, or perform synchronous state transitions.

## Historical backfill

Run a bounded backfill over high-signal commits, test commands, failures, and
session boundaries. Do not replay every historical raw event immediately.
Backfill observations are labeled so they cannot silently dominate live
evidence or the initial baseline.

## Failure handling and privacy

If background semantic analysis fails, the observation remains persisted and
the coaching item is marked pending/failed for retry. Normal development and
event capture continue. Generic advice is never substituted for failed
personalized coaching.

Evidence stores source IDs and bounded sanitized excerpts only. Existing local
encryption and sanitization are reused. Raw transmission to a configured
provider is bounded by provider settings and never required for deterministic
observation detection.

## Testing and evaluation

Unit tests cover command extraction, scope matching, change classification,
confidence, counter-evidence, state transitions, and feedback resolution.

Fixtures cover targeted/broad/unrelated tests, tests before changes, delayed
verification, regressions, documentation-only changes, generated/vendor
changes, missing hook events, contradictory evidence, backfill, and
cross-project transfer.

Integration tests cover migrations, asynchronous processing, non-blocking
hooks, CLI mutation, stable MCP JSON, and compatibility with existing profile,
distillation, and hook behavior.

The feature is observation-only behind a flag until these checks pass. Pilot
success requires at least 20 sessions and measurable improvement in repeated
verification failures, relevant test rate, independent applications, and
false-positive/dismissal rate before adding another skill domain.

Suggested modes:

```text
FORGE_COACH_MODE=off|observe|quiet|normal|strict
```

## Rollout

1. Add schema, repository methods, and deterministic detector behind a feature
   flag.
2. Add fixture tests and `forge coach` review commands.
3. Add background observation processing and state transitions.
4. Add queued safe-boundary suggestions.
5. Add read-only MCP access.
6. Run the 20-session pilot.
7. Tune thresholds before expanding to debugging, security, architecture, or
   other coaching domains.

## Out of scope

- Personality, motivation, intelligence, or mental-health inference.
- Developer rankings or anonymous percentile comparisons.
- Automatic test generation as the primary teaching mechanism.
- Full raw-transcript semantic processing for every event.
- A separate graph database or second event-sourcing platform.
- A futuristic dashboard before the coaching loop is validated.
