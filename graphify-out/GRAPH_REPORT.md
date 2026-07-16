# Graph Report - forgememo-cli  (2026-07-15)

## Corpus Check
- 144 files · ~134,508 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1821 nodes · 4264 edges · 95 communities (73 shown, 22 thin omitted)
- Extraction: 81% EXTRACTED · 19% INFERRED · 0% AMBIGUOUS · INFERRED: 815 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `fdfbf16a`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- captureStdout
- daemon.go
- main.go
- Open
- hook.go
- pipeline.ts
- integration_test.go
- server.go
- runConfig
- distill_test.go
- withServiceSeamsReset
- main
- planner.go
- server_test.go
- init_integrations.go
- failures.go
- distill.go
- package.json
- DB
- Architecture
- agent_test.go
- context7_mcp.go
- ai_history.go
- .fetchExaSearch
- worker_test.go
- multiinstall_test.go
- principles_test.go
- context7MCPClient
- DB
- setupOpencode
- worker.go
- dependencies
- Client
- Changelog
- Distiller
- socket_test.go
- forge.js
- RetrievalJob
- Event
- compilerOptions
- ForgePath
- New
- Run
- lifecycle_security_integrity.sh
- setupCodex
- CrossSessionPattern
- openAlertsDB
- setupAntigravity
- maybeRefineContext7Hint
- lifecycle_realworld.sh
- .GetRecentSessionSummariesByProject
- agent.go
- cross_session_test.go
- Forgememo - Silent Memory Layer for AI Agents
- CLAUDE.md
- Internal Service Test PR Plan
- .RecordDistillationFailure
- lifecycle_full.sh
- configureUnixBackground
- .detectAndMarkConflicts
- .InsertEvent
- pipe_windows.go
- download-report.sh
- [0.3.0] - 2025-04-03
- [0.4.13] - 2026-04-05
- [0.4.14] - 2026-04-05
- [0.4.23] - 2026-04-07
- TestVersionFileMatchesGitTag
- Forge LLM Provider & Model Matrix
- Forge
- [0.4.11] - 2026-04-05
- [0.4.38] - 2026-05-07
- [0.4.6] - 2026-04-04
- [0.5.11] - 2026-07-01
- [0.5.13] - 2026-07-01
- [0.5.3] - 2026-05-29
- [0.5.8] - 2026-06-21
- startBackground
- test_integration.sh
- [0.4.10] - 2026-04-04
- [0.4.4] - 2026-04-03
- [0.4.8] - 2026-04-04
- [0.5.12] - 2026-07-01
- [0.5.15] - 2026-07-01
- [0.5.4] - 2026-06-17
- [0.5.5] - 2026-06-17
- [0.5.7] - 2026-06-20
- [0.5.9] - 2026-06-30
- [0.6.0] - 2026-07-01
- [0.6.2] - 2026-07-02
- [0.6.3] - 2026-07-10
- install.sh script
- github.com/forge/forge
- github.com/forge/forge/payment

## God Nodes (most connected - your core abstractions)
1. `Open()` - 120 edges
2. `shortHome()` - 43 edges
3. `captureStdout()` - 39 edges
4. `runDistill()` - 37 edges
5. `Distiller` - 35 edges
6. `Changelog` - 33 edges
7. `runForge()` - 32 edges
8. `runDoctor()` - 30 edges
9. `Request` - 30 edges
10. `main()` - 29 edges

## Surprising Connections (you probably didn't know these)
- `runDaemon()` --calls--> `SetImmediateWakeChannel()`  [INFERRED]
  cmd/daemon.go → internal/retrieve/runtime.go
- `runConfig()` --calls--> `UserMessage()`  [INFERRED]
  cmd/config_cmd.go → internal/distill/distill.go
- `runConfig()` --calls--> `ValidateConfig()`  [INFERRED]
  cmd/config_cmd.go → internal/distill/distill.go
- `distillConfigFromUserConfig()` --calls--> `LoadConfig()`  [INFERRED]
  cmd/config_cmd.go → internal/distill/distill.go
- `runDaemon()` --calls--> `Load()`  [INFERRED]
  cmd/daemon.go → internal/config/config.go

## Import Cycles
- None detected.

## Communities (95 total, 22 thin omitted)

### Community 0 - "captureStdout"
Cohesion: 0.07
Nodes (87): acquireDistillLock(), cleanDistillLock(), distillLockPath(), isDistillLockStale(), Config, DB, Time, hasExplicitInferenceProviderConfig() (+79 more)

### Community 1 - "daemon.go"
Cohesion: 0.05
Nodes (91): acquireDaemonLock(), acquirePIDLock(), acquireStartupLock(), cleanAddr(), cleanLock(), cleanPID(), cleanSocket(), cleanStartupLock() (+83 more)

### Community 2 - "main.go"
Cohesion: 0.06
Nodes (70): conflictPairJSON, principleJSON, Server, Handler, DB, Principle, ResponseWriter, New() (+62 more)

### Community 3 - "Open"
Cohesion: 0.11
Nodes (46): T, TestInsertCrossSessionPattern_Defaults(), TestMarkCrossSessionSynthesized_Batch(), TestProjectIDsWithEnoughSessions_MinThreshold(), TestRecentCrossSessionPatternsByProject_PathVariants(), TestUnSynthesizedPatterns_OnlyUnsynthesized(), DB, Principle (+38 more)

### Community 4 - "hook.go"
Cohesion: 0.07
Nodes (74): ClaudeHookInput, buildSessionRecallOutput(), checkpointKey(), collectPromptCandidates(), compactWhitespace(), confidenceLabel(), displayProjectName(), envInt() (+66 more)

### Community 5 - "pipeline.ts"
Cohesion: 0.06
Nodes (56): DB_PATH, DistillationHealth, encodeStringSlice(), Event, filterConcepts(), fingerprint(), hasSessionCheckpoint(), insertPrinciple() (+48 more)

### Community 6 - "integration_test.go"
Cohesion: 0.09
Nodes (66): baseEnv(), copyFile(), Cmd, Duration, T, Writer, killProcessTree(), runForge() (+58 more)

### Community 7 - "server.go"
Cohesion: 0.08
Nodes (46): T, TestEventRecencyScore(), TestEventRecencyScore_InvalidTS(), TestFallback(), TestIntFromArgs(), TestStringFromArgs(), TestTokenSet(), TestTokenSet_ShortTokensExcluded() (+38 more)

### Community 8 - "runConfig"
Cohesion: 0.06
Nodes (58): T, resolveGoEnv(), TestRunConfig_AutoValidatesAndRejectsBadKey(), TestRunConfig_NoValidateSkipsNetwork(), T, TestRunConfig_Antigravity(), TestRunConfig_DefaultModelAnthropicHaiku(), TestRunConfig_DistillBatchSize() (+50 more)

### Community 9 - "distill_test.go"
Cohesion: 0.09
Nodes (45): runSynthesizeSession(), testError, LoadConfig(), parseConflictPairs(), parseIntOrDefault(), containsStr(), containsStrHelper(), T (+37 more)

### Community 10 - "withServiceSeamsReset"
Cohesion: 0.10
Nodes (26): T, TestReadInstalledBinaryPath_NotInstalled(), TestReadInstalledBinaryPath_UnsupportedOS(), TestReadLaunchdBinaryPath_MalformedPlist(), TestReadLaunchdBinaryPath_MismatchDetected(), TestReadLaunchdBinaryPath_RoundTrip(), TestReadSystemdBinaryPath_MissingExecStart(), TestReadSystemdBinaryPath_RoundTrip() (+18 more)

### Community 11 - "main"
Cohesion: 0.30
Nodes (14): exitPanic, expectExitCode(), T, TestRunServiceInstall_AlreadyInstalled(), TestRunServiceInstall_InstallErrorExits(), TestRunServiceStart_ManagerCreationErrorExits(), TestRunServiceStart_StartErrorExits(), TestRunServiceStop_Success() (+6 more)

### Community 12 - "planner.go"
Cohesion: 0.06
Nodes (63): runDashboard(), defaultPath(), DB, Open(), OpenReadOnly(), readonlyDBError(), T, TestDistillationHealth_RecordSuccessAndFailure() (+55 more)

### Community 13 - "server_test.go"
Cohesion: 0.15
Nodes (28): Alert, FailureSignature, DB, scanAlerts(), New(), callToolText(), DB, RawMessage (+20 more)

### Community 14 - "init_integrations.go"
Cohesion: 0.14
Nodes (27): ClientAdapter, contains(), checkAndRepairIntegrationPaths(), confirmWrite(), fileSHA256(), findStaleForgePathInHooks(), isEquivalentBinary(), isTTY() (+19 more)

### Community 15 - "failures.go"
Cohesion: 0.14
Nodes (32): failureObservation, successObservation, buildAlertTitle(), candidateLines(), classifyFailure(), commandFamily(), extractCommandFamily(), extractStrings() (+24 more)

### Community 16 - "distill.go"
Cohesion: 0.13
Nodes (25): distillConfigFromUserConfig(), Config, runInjectCheck(), Config, Provider, TranscriptStep, cleanFilePath(), compactWhitespace() (+17 more)

### Community 17 - "package.json"
Cohesion: 0.09
Nodes (24): bin, forgememo, description, engines, node, files, homepage, keywords (+16 more)

### Community 18 - "DB"
Cohesion: 0.05
Nodes (54): printHardenUsage(), runHarden(), runHardenRevoke(), runHardenRotateKey(), Principle, testError, DB, Rows (+46 more)

### Community 19 - "Architecture"
Cohesion: 0.08
Nodes (23): Agent Integration Points, Architecture, Binary Architecture, CLI Commands, ✅ Completed (v0.1.0), Data Model (SQLite + FTS5), Feasibility Assessment (April 2026), Forge — Silent Memory Forger: Feasibility & Plan (+15 more)

### Community 20 - "agent_test.go"
Cohesion: 0.19
Nodes (17): claudeAdapter, T, TestProbeWritable_ReadOnlyDir(), TestProbeWritable_WritableDir(), TestSetupClaude_IdempotentInit(), TestSetupClaude_NoDuplicateHooks(), TestSetupClaude_PreservesUserSettings(), TestSetupClaude_RegistersUserPromptSubmit() (+9 more)

### Community 21 - "context7_mcp.go"
Cohesion: 0.15
Nodes (22): aiSummaryKind(), aiSummaryTags(), maybeRefineContext7Hint(), parseAIHintDecision(), shouldUseAIHintRefinement(), buildExaNarrative(), buildWebQuery(), exaAPIKey() (+14 more)

### Community 22 - "ai_history.go"
Cohesion: 0.19
Nodes (16): DB, hashContent(), loadHashes(), payloadJSON(), saveHashes(), T, TestLoadSaveHashes_RoundTrip(), TestPayloadJSON_InvalidMapFallsBackToObject() (+8 more)

### Community 23 - ".fetchExaSearch"
Cohesion: 0.13
Nodes (6): Event, ProjectTimelineEntry, DB, ScrubSecrets(), T, TestScrubSecrets()

### Community 24 - "worker_test.go"
Cohesion: 0.19
Nodes (20): NewWorker(), context7SetupTestServer(), Reader, Server, T, Writer, readMCPTestMessage(), TestContext7LibraryIDFromQuery() (+12 more)

### Community 25 - "multiinstall_test.go"
Cohesion: 0.24
Nodes (18): ForgeInstall, findForgeInstalls(), isManagedForgePath(), queryForgeVersion(), T, TestFindForgeInstalls_DeduplicatesSameRealPath(), TestFindForgeInstalls_EmptyPATH(), TestFindForgeInstalls_FindsForgememoName() (+10 more)

### Community 26 - "principles_test.go"
Cohesion: 0.15
Nodes (13): Buffer, Context, context7MCPCommand(), context7MCPErrorf(), context7SummaryLibraryName(), context7ToolName(), Cmd, Reader (+5 more)

### Community 27 - "context7MCPClient"
Cohesion: 0.09
Nodes (22): 🔒 ForgeMemo CLI — Hardening Roadmap, P0 — Key Rotation & Compromise Recovery ✅ done, P0 — Keychain-Backed Signing Key ✅ done, P0 — Principles at Rest: Sign Every Stored Principle ✅ done, P1 — Authenticated Unix Socket ❌ attempted, reverted — not worth it as scoped, P1 — Escalation Path Audit, P1 — Event Signing at Capture Time, P1 — Immutable Event Log (Chain-of-Hash) (+14 more)

### Community 28 - "DB"
Cohesion: 0.19
Nodes (18): ExternalContextSummary, RetrievalJob, webSummaryKind(), compactText(), context7Section(), fetchPageText(), DB, Duration (+10 more)

### Community 29 - "setupOpencode"
Cohesion: 0.22
Nodes (13): opencodeAdapter, opencodeConfigDir(), setupOpencode(), T, TestOpencodeAdapter_DetectByDir(), TestOpencodeAdapter_DetectByXDG(), TestOpencodeAdapter_DetectMissing(), TestSetupOpencode_CreatesConfigDirIfMissing() (+5 more)

### Community 30 - "worker.go"
Cohesion: 0.50
Nodes (8): DB, printMemoryUsage(), runMemory(), runMemoryDelete(), runMemoryList(), runMemoryRate(), runMemoryThumbsDown(), runMemoryThumbsUp()

### Community 31 - "dependencies"
Cohesion: 0.11
Nodes (17): dependencies, @ai-sdk/anthropic, @ai-sdk/openai, better-sqlite3, @mastra/core, ollama-ai-provider, uuid, zod (+9 more)

### Community 32 - "Client"
Cohesion: 0.18
Nodes (10): Cmd, Duration, Mutex, RawMessage, Reader, WriteCloser, joinStrings(), NewClient() (+2 more)

### Community 33 - "Changelog"
Cohesion: 0.12
Nodes (15): [0.4.10] - 2026-04-04, [0.4.4] - 2026-04-03, [0.4.5] - 2026-04-03, [0.5.6] - 2026-06-20, [0.5.9] - 2026-06-30, [0.6.1] - 2026-07-01, [0.6.3] - 2026-07-10, Changelog (+7 more)

### Community 34 - "Distiller"
Cohesion: 0.22
Nodes (7): codexCommand(), isProviderUnreachableError(), maxInt(), normalizeOpenAIBase(), shouldRetryOllama(), TestIsProviderUnreachableError_WindowsConnectex(), TestNormalizeOpenAIBase()

### Community 35 - "socket_test.go"
Cohesion: 0.22
Nodes (14): Listener, IsDaemonAlive(), Listen(), Send(), socketPath(), contains(), containsHelper(), T (+6 more)

### Community 36 - "forge.js"
Cohesion: 0.20
Nodes (15): args, compareVersions(), findAllInPath(), fs, getBinaryVersion(), localBinary, os, parseVersion() (+7 more)

### Community 37 - "RetrievalJob"
Cohesion: 0.21
Nodes (22): buildAIHintPrompt(), topContext7Excerpts(), anyString(), appendContext7Text(), bestContext7Sentence(), cleanContext7Sentence(), collectContext7Texts(), compactContext7Narrative() (+14 more)

### Community 38 - "Event"
Cohesion: 0.15
Nodes (10): Distiller, UsageStats, firstNonEmptyString(), Event, SessionSummary, isSessionBoundaryEvent(), minInt(), selectDistillationBatch() (+2 more)

### Community 39 - "compilerOptions"
Cohesion: 0.14
Nodes (13): compilerOptions, declaration, esModuleInterop, forceConsistentCasingInFileNames, module, moduleResolution, outDir, resolveJsonModule (+5 more)

### Community 40 - "ForgePath"
Cohesion: 0.23
Nodes (9): geminiAdapter, ForgePath(), TestSetupGemini_RegistersHooksAndPreservesSettings(), TestSetupGemini_RewritesStaleForgeHookPath(), TestUpsertCommandHookArray_CollapseDuplicates(), isAnyForgeCommandHookItem(), isForgeCommandHookItem(), setupGemini() (+1 more)

### Community 42 - "Run"
Cohesion: 0.27
Nodes (11): buildPayload(), findRepos(), DB, Duration, recentCommits(), Run(), T, TestBuildPayload() (+3 more)

### Community 43 - "lifecycle_security_integrity.sh"
Cohesion: 0.42
Nodes (10): assert_contains(), assert_eq(), assert_ge(), assert_le(), assert_not_contains(), fail(), info(), ok() (+2 more)

### Community 44 - "setupCodex"
Cohesion: 0.24
Nodes (8): codexAdapter, hasCodex(), TestSetupCodex_HonorsCODEX_HOME(), TestSetupCodex_WritesExplicitVerificationAndPostToolUseHook(), manualCodexFallback(), probeWritable(), registerCodexMCP(), setupCodex()

### Community 45 - "CrossSessionPattern"
Cohesion: 0.26
Nodes (5): CrossSessionPattern, boolToInt(), DB, Rows, scanCrossSessionPatterns()

### Community 46 - "openAlertsDB"
Cohesion: 0.41
Nodes (11): DB, T, openAlertsDB(), TestAcknowledgeAlerts(), TestAcknowledgeAlerts_EmptySourceRefs(), TestActiveAlertsByProject_EmptyProjectReturnsAll(), TestResolveFailureSignatures(), TestResolveFailureSignatures_EmptyArgs() (+3 more)

### Community 47 - "setupAntigravity"
Cohesion: 0.24
Nodes (6): antigravityAdapter, setupAntigravity(), T, TestAntigravityAdapter_DetectByDir(), TestAntigravityAdapter_DetectMissing(), TestSetupAntigravity_WritesMcpAndHooks()

### Community 49 - "lifecycle_realworld.sh"
Cohesion: 0.47
Nodes (9): assert_contains(), assert_eq(), assert_ge(), assert_not_contains(), fail(), info(), ok(), lifecycle_realworld.sh script (+1 more)

### Community 50 - ".GetRecentSessionSummariesByProject"
Cohesion: 0.33
Nodes (4): SessionSummary, DB, Rows, scanSessionSummaries()

### Community 51 - "agent.go"
Cohesion: 0.22
Nodes (7): FileMode, IsStableExecutablePath(), IsWindows(), stableExecutablePath(), TestStableExecutablePath_AcceptsInstalledBinary(), TestStableExecutablePath_RejectsEphemeralBuildArtifacts(), WriteFileConfirm()

### Community 52 - "cross_session_test.go"
Cohesion: 0.21
Nodes (15): crossSessionMockServer(), Int32, Server, T, TestParseCrossSessionPatterns_InvalidType_DefaultsWorkflow(), TestParseCrossSessionPatterns_SkipsEmptyPattern(), TestParseCrossSessionPatterns_ValidArray(), TestSynthesizeCrossSession_InsertsPatterns() (+7 more)

### Community 53 - "Forgememo - Silent Memory Layer for AI Agents"
Cohesion: 0.20
Nodes (9): Codebase Architecture, Configuration Precedence & Caching, Directory Structure, Distillation Threshold, Documentation Reference, Forgememo - Silent Memory Layer for AI Agents, Quick Install, Quick Start (+1 more)

### Community 54 - "CLAUDE.md"
Cohesion: 0.22
Nodes (7): CLI login flow, Local dev, Payment server routes, Release, Repomix packing, Structure, Testing CLI install (Lima VM)

### Community 55 - "Internal Service Test PR Plan"
Cohesion: 0.22
Nodes (8): Current Risk Snapshot, Implementation Notes, Internal Service Test PR Plan, Non-Goals, Objective, PR Scope (Focused), Suggested Acceptance Criteria, Test Matrix

### Community 56 - ".RecordDistillationFailure"
Cohesion: 0.32
Nodes (4): DistillationHealth, DB, Duration, Time

### Community 57 - "lifecycle_full.sh"
Cohesion: 0.57
Nodes (6): assert_contains(), assert_ge(), fail(), ok(), lifecycle_full.sh script, step()

### Community 58 - "configureUnixBackground"
Cohesion: 0.38
Nodes (5): configureUnixBackground(), Cmd, startBackground(), T, TestConfigureUnixBackground_DetachesUnixProcess()

### Community 59 - ".detectAndMarkConflicts"
Cohesion: 0.38
Nodes (5): FilterConcepts(), buildConflictPrompt(), Principle, shouldKeepPrinciple(), TestBuildConflictPrompt_ContainsPrincipleIDs()

### Community 61 - "pipe_windows.go"
Cohesion: 0.47
Nodes (5): Listener, IsDaemonAlive(), Listen(), pipeAddr(), Send()

### Community 63 - "[0.3.0] - 2025-04-03"
Cohesion: 0.50
Nodes (4): [0.3.0] - 2025-04-03, Added, Changed, Fixed

### Community 64 - "[0.4.13] - 2026-04-05"
Cohesion: 0.50
Nodes (4): [0.4.13] - 2026-04-05, Added, Changed, Fixed

### Community 65 - "[0.4.14] - 2026-04-05"
Cohesion: 0.50
Nodes (4): [0.4.14] - 2026-04-05, Added, Changed, Fixed

### Community 66 - "[0.4.23] - 2026-04-07"
Cohesion: 0.50
Nodes (4): [0.4.23] - 2026-04-07, Added, Changed, Fixed

### Community 67 - "TestVersionFileMatchesGitTag"
Cohesion: 0.67
Nodes (3): findRepoRoot(), T, TestVersionFileMatchesGitTag()

### Community 68 - "Forge LLM Provider & Model Matrix"
Cohesion: 0.50
Nodes (3): Forge LLM Provider & Model Matrix, Supported Configuration Matrix, Validation Behavior

### Community 70 - "[0.4.11] - 2026-04-05"
Cohesion: 0.67
Nodes (3): [0.4.11] - 2026-04-05, Added, Fixed

### Community 71 - "[0.4.38] - 2026-05-07"
Cohesion: 0.67
Nodes (3): [0.4.38] - 2026-05-07, Added, Fixed (OpenCode compatibility round)

### Community 72 - "[0.4.6] - 2026-04-04"
Cohesion: 0.67
Nodes (3): [0.4.6] - 2026-04-04, Added, Fixed

### Community 73 - "[0.5.11] - 2026-07-01"
Cohesion: 0.67
Nodes (3): [0.5.11] - 2026-07-01, Added, Fixed

### Community 74 - "[0.5.13] - 2026-07-01"
Cohesion: 0.67
Nodes (3): [0.5.13] - 2026-07-01, Added, Fixed

### Community 75 - "[0.5.3] - 2026-05-29"
Cohesion: 0.67
Nodes (3): [0.5.3] - 2026-05-29, Added, Fixed

### Community 76 - "[0.5.8] - 2026-06-21"
Cohesion: 0.67
Nodes (3): [0.5.8] - 2026-06-21, Added, Fixed

## Knowledge Gaps
- **165 isolated node(s):** `HookMessage`, `exitPanic`, `github.com/forge/forge`, `install.sh script`, `TranscriptStep` (+160 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **22 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Open()` connect `planner.go` to `captureStdout`, `daemon.go`, `main.go`, `Open`, `hook.go`, `integration_test.go`, `distill_test.go`, `server_test.go`, `init_integrations.go`, `openAlertsDB`, `failures.go`, `DB`, `cross_session_test.go`, `ai_history.go`, `worker_test.go`, `[0.6.3] - 2026-07-10`, `worker.go`?**
  _High betweenness centrality (0.311) - this node is a cross-community bridge._
- **Why does `runDoctor()` connect `daemon.go` to `captureStdout`, `ForgePath`, `distill_test.go`, `planner.go`, `multiinstall_test.go`?**
  _High betweenness centrality (0.104) - this node is a cross-community bridge._
- **Why does `ForgePath()` connect `ForgePath` to `captureStdout`, `daemon.go`, `setupCodex`, `init_integrations.go`, `setupAntigravity`, `agent.go`, `agent_test.go`, `setupOpencode`?**
  _High betweenness centrality (0.063) - this node is a cross-community bridge._
- **Are the 113 inferred relationships involving `Open()` (e.g. with `collectStatusReport()` and `noteDistillSkip()`) actually correct?**
  _`Open()` has 113 INFERRED edges - model-reasoned connections that need verification._
- **Are the 21 inferred relationships involving `captureStdout()` (e.g. with `TestRunConfig_ShowJSON()` and `captureStdoutString()`) actually correct?**
  _`captureStdout()` has 21 INFERRED edges - model-reasoned connections that need verification._
- **What connects `HookMessage`, `exitPanic`, `github.com/forge/forge` to the rest of the system?**
  _165 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `captureStdout` be split into smaller, more focused modules?**
  _Cohesion score 0.06965871902758299 - nodes in this community are weakly interconnected._