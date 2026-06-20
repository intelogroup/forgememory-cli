import Database from "better-sqlite3";

import { ProviderConfig } from "./provider.js";
import { callLLM } from "./llm.js";
import {
  buildSessionPrompt,
  buildCheckpointPrinciplesPrompt,
  buildChunkSummaryPrompt,
  buildMasterSummaryPrompt,
  buildChunkPrinciplesPrompt,
} from "./prompts.js";
import type { ChunkSummary } from "./prompts.js";
import { parseSessionSummary, parseChunkSummary, parsePrinciples } from "./parsers.js";
import {
  sessionEventsUpTo,
  markSessionDistilled,
  hasSessionCheckpoint,
  insertSessionSummary,
  insertPrinciple,
  recordDistillationSuccess,
  recordDistillationFailure,
  type Event,
  type SessionSummary,
  type Principle,
} from "./db.js";

const CHUNK_SIZE = 50;
const DIRECT_CUTOFF = 100;

export interface PipelineOpts {
  sessionID: string;
  projectID: string;
  checkpointKind: string;
  checkpointKey: string;
  cutoffTS?: string;
  contextBlock?: string | null;
  db: Database.Database;
  llmCfg: ProviderConfig;
}

export async function runPipeline(opts: PipelineOpts): Promise<void> {
  const {
    sessionID,
    projectID,
    checkpointKind,
    checkpointKey,
    cutoffTS,
    contextBlock,
    db,
    llmCfg,
  } = opts;

  if (checkpointKey && hasSessionCheckpoint(db, checkpointKey)) return;

  const events = sessionEventsUpTo(db, sessionID, cutoffTS);
  if (events.length < 3) {
    markSessionDistilled(db, sessionID);
    return;
  }

  const proj = projectID || events[0].project_id;
  const runAt = new Date();
  const startTime = Date.now();

  try {
    let principlesCount: number;

    if (events.length <= DIRECT_CUTOFF) {
      principlesCount = await processDirect(
        events, proj, checkpointKind, checkpointKey, contextBlock, db, llmCfg
      );
    } else {
      principlesCount = await processLargeSession(
        events, proj, checkpointKind, checkpointKey, contextBlock, db, llmCfg
      );
    }

    markSessionDistilled(db, sessionID);
    const durationMs = Date.now() - startTime;
    recordDistillationSuccess(
      db, runAt, durationMs, events.length, principlesCount,
      new Date(Date.now() + 600000)
    );
    console.error(
      `distill-agent: session ${sessionID} → ${events.length} events, ${principlesCount} principle(s) (context: ${!!contextBlock})`
    );
  } catch (err) {
    const durationMs = Date.now() - startTime;
    const errMsg = err instanceof Error ? err.message : String(err);
    recordDistillationFailure(
      db, runAt, durationMs, events.length, errMsg,
      new Date(Date.now() + 600000)
    );
    console.error(`distill-agent: session ${sessionID} failed: ${errMsg}`);
    process.exit(1);
  }
}

async function processDirect(
  events: Event[],
  projectID: string,
  checkpointKind: string,
  checkpointKey: string,
  contextBlock: string | null | undefined,
  db: Database.Database,
  llmCfg: ProviderConfig
): Promise<number> {
  const sessionPrompt = buildSessionPrompt(events, contextBlock || undefined);
  const sessionResp = await callLLM(
    llmCfg,
    "You are a senior engineer analyzing coding sessions. Return JSON only.",
    sessionPrompt
  );
  const summaryResult = parseSessionSummary(sessionResp);

  const summary: SessionSummary = {
    id: "",
    ts: new Date().toISOString(),
    session_id: events[0].session_id,
    project_id: projectID,
    checkpoint_kind: checkpointKind,
    checkpoint_key: checkpointKey,
    request: summaryResult.request,
    investigation: summaryResult.investigation,
    learnings: summaryResult.learnings,
    next_steps: summaryResult.next_steps,
    keywords: Array.isArray(summaryResult.keywords)
      ? summaryResult.keywords.join(" ")
      : "",
    summary: summaryResult.learnings,
    tokens: 0,
  };
  insertSessionSummary(db, summary);

  const principlesPrompt = buildCheckpointPrinciplesPrompt(
    summaryResult.request,
    summaryResult.investigation,
    summaryResult.learnings,
    summaryResult.next_steps,
    summary.keywords,
    contextBlock || undefined
  );
  const principlesResp = await callLLM(
    llmCfg,
    "You are a senior staff engineer extracting engineering principles. Return JSON array only.",
    principlesPrompt
  );
  const principles = parsePrinciples(principlesResp);

  let inserted = 0;
  for (const p of principles) {
    const principle: Principle = {
      id: "",
      ts: new Date().toISOString(),
      type: p.type,
      title: p.title,
      narrative: p.narrative,
      impact_score: p.impact_score,
      project_id: projectID,
      source_event: "",
      fingerprint: "",
      concepts: p.concepts,
      files_modified: p.files_modified,
      status: "",
      conflict_peer_id: "",
      outcome: p.outcome,
      impl_hint: p.impl_hint,
      last_used_ts: "",
      use_count: 0,
    };
    if (insertPrinciple(db, principle)) inserted++;
  }

  return inserted;
}

async function processLargeSession(
  events: Event[],
  projectID: string,
  checkpointKind: string,
  checkpointKey: string,
  contextBlock: string | null | undefined,
  db: Database.Database,
  llmCfg: ProviderConfig
): Promise<number> {
  const chunks: Event[][] = [];
  for (let i = 0; i < events.length; i += CHUNK_SIZE) {
    chunks.push(events.slice(i, i + CHUNK_SIZE));
  }

  const chunkSummaries: ChunkSummary[] = [];
  for (let i = 0; i < chunks.length; i++) {
    const prompt = buildChunkSummaryPrompt(chunks[i], i + 1, chunks.length);
    const resp = await callLLM(
      llmCfg,
      "You are a senior engineer analyzing coding sessions. Return JSON only.",
      prompt
    );
    const cs = parseChunkSummary(resp);
    cs.index = i;
    cs.events = chunks[i].length;
    chunkSummaries.push(cs);
  }

  const masterPrompt = buildMasterSummaryPrompt(chunkSummaries);
  const masterResp = await callLLM(
    llmCfg,
    "You are a senior engineer synthesizing session summaries. Return JSON only.",
    masterPrompt
  );
  const masterResult = parseSessionSummary(masterResp);

  const masterSummary: SessionSummary = {
    id: "",
    ts: new Date().toISOString(),
    session_id: events[0].session_id,
    project_id: projectID,
    checkpoint_kind: checkpointKind,
    checkpoint_key: checkpointKey,
    request: masterResult.request,
    investigation: masterResult.investigation,
    learnings: masterResult.learnings,
    next_steps: masterResult.next_steps,
    keywords: Array.isArray(masterResult.keywords)
      ? masterResult.keywords.join(" ")
      : "",
    summary: masterResult.learnings,
    tokens: 0,
  };
  insertSessionSummary(db, masterSummary);

  const principlesPrompt = buildChunkPrinciplesPrompt(
    {
      index: 0,
      events: events.length,
      request: masterResult.request,
      investigation: masterResult.investigation,
      learnings: masterResult.learnings,
      nextSteps: masterResult.next_steps,
      keywords: Array.isArray(masterResult.keywords)
        ? masterResult.keywords
        : [],
    },
    chunkSummaries
  );
  const principlesResp = await callLLM(
    llmCfg,
    "You are a senior staff engineer extracting engineering principles. Return JSON array only.",
    principlesPrompt
  );
  const principles = parsePrinciples(principlesResp);

  let inserted = 0;
  for (const p of principles) {
    const principle: Principle = {
      id: "",
      ts: new Date().toISOString(),
      type: p.type,
      title: p.title,
      narrative: p.narrative,
      impact_score: p.impact_score,
      project_id: projectID,
      source_event: "",
      fingerprint: "",
      concepts: p.concepts,
      files_modified: p.files_modified,
      status: "",
      conflict_peer_id: "",
      outcome: p.outcome,
      impl_hint: p.impl_hint,
      last_used_ts: "",
      use_count: 0,
    };
    if (insertPrinciple(db, principle)) inserted++;
  }

  return inserted;
}
