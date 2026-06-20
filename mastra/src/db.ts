import Database from "better-sqlite3";
import path from "node:path";
import os from "node:os";
import { v4 as uuid } from "uuid";

const DB_PATH = path.join(os.homedir(), ".forge", "forge.db");

export interface Event {
  id: string;
  ts: string;
  session_id: string;
  project_id: string;
  source_tool: string;
  event_type: string;
  tool_name: string;
  payload: string;
  distilled: boolean;
}

export interface SessionSummary {
  id: string;
  ts: string;
  session_id: string;
  project_id: string;
  checkpoint_kind: string;
  checkpoint_key: string;
  summary: string;
  request: string;
  investigation: string;
  learnings: string;
  next_steps: string;
  keywords: string;
  tokens: number;
}

export interface Principle {
  id: string;
  ts: string;
  type: string;
  title: string;
  narrative: string;
  impact_score: number;
  project_id: string;
  source_event: string;
  fingerprint: string;
  concepts: string[];
  files_modified: string[];
  status: string;
  conflict_peer_id: string;
  outcome: string;
  impl_hint: string;
  last_used_ts: string;
  use_count: number;
}

export interface DistillationHealth {
  last_run_at: string;
  last_success_at: string;
  last_error_at: string;
  last_error_message: string;
  last_status: string;
  consecutive_failures: number;
  total_successes: number;
  total_failures: number;
  last_attempted_events: number;
  last_distilled_principles: number;
  last_duration_ms: number;
  next_scheduled_at: string;
}

export function openDB(dbPath?: string): Database.Database {
  const fullPath = dbPath || DB_PATH;
  const db = new Database(fullPath);
  db.pragma("journal_mode = WAL");
  return db;
}

export function sessionEventsUpTo(
  db: Database.Database,
  sessionID: string,
  cutoffTS?: string,
  limit?: number
): Event[] {
  let query = `SELECT id, ts, session_id, project_id, source_tool, event_type, tool_name, payload, distilled
       FROM events WHERE session_id=?`;
  const args: unknown[] = [sessionID];
  if (cutoffTS) {
    query += ` AND ts <= ?`;
    args.push(cutoffTS);
  }
  query += ` ORDER BY ts ASC`;
  if (limit && limit > 0) {
    query += ` LIMIT ?`;
    args.push(limit);
  }
  return db.prepare(query).all(...args) as Event[];
}

export function markSessionDistilled(
  db: Database.Database,
  sessionID: string
): void {
  db.prepare("UPDATE events SET distilled=1 WHERE session_id=?").run(
    sessionID
  );
}

export function hasSessionCheckpoint(
  db: Database.Database,
  checkpointKey: string
): boolean {
  if (!checkpointKey) return false;
  const row = db
    .prepare(
      "SELECT COUNT(*) as cnt FROM session_summaries WHERE checkpoint_key = ?"
    )
    .get(checkpointKey) as { cnt: number } | undefined;
  return row ? row.cnt > 0 : false;
}

export function insertSessionSummary(
  db: Database.Database,
  s: SessionSummary
): void {
  if (!s.id) s.id = uuid();
  if (!s.ts) s.ts = new Date().toISOString();
  if (!s.summary && s.learnings) s.summary = s.learnings;
  db.prepare(
    `INSERT INTO session_summaries
     (id, ts, session_id, project_id, checkpoint_kind, checkpoint_key, summary, request, investigation, learnings, next_steps, keywords, tokens)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  ).run(
    s.id,
    s.ts,
    s.session_id,
    s.project_id,
    s.checkpoint_kind,
    s.checkpoint_key,
    s.summary,
    s.request,
    s.investigation,
    s.learnings,
    s.next_steps,
    s.keywords,
    s.tokens
  );
}

function fingerprint(title: string, projectID: string): string {
  const crypto = require("node:crypto");
  return crypto
    .createHash("sha256")
    .update((title || "").toLowerCase() + "|" + (projectID || ""))
    .digest("hex")
    .slice(0, 16);
}

function encodeStringSlice(arr: string[] | undefined | null): string {
  if (!arr || arr.length === 0) return "[]";
  return JSON.stringify(arr);
}

const VALID_CONCEPTS = new Set([
  "security",
  "pattern",
  "gotcha",
  "performance",
  "trade-off",
  "how-it-works",
]);

export function filterConcepts(concepts: string[] | undefined | null): string[] {
  if (!concepts) return [];
  return concepts.filter((c) => VALID_CONCEPTS.has(c));
}

export function insertPrinciple(
  db: Database.Database,
  p: Principle
): boolean {
  if (!p.id) p.id = uuid();
  if (!p.ts) p.ts = new Date().toISOString();
  if (!p.fingerprint) {
    p.fingerprint = fingerprint(p.title, p.project_id);
  }
  if (!p.outcome) p.outcome = "unknown";
  const conceptsJSON = encodeStringSlice(p.concepts);
  const filesJSON = encodeStringSlice(p.files_modified);

  try {
    const result = db
      .prepare(
        `INSERT OR IGNORE INTO principles
       (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint, concepts, files_modified, outcome, impl_hint, last_used_ts, use_count)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
      )
      .run(
        p.id,
        p.ts,
        p.type,
        p.title,
        p.narrative,
        p.impact_score,
        p.project_id,
        p.source_event,
        p.fingerprint,
        conceptsJSON,
        filesJSON,
        p.outcome,
        p.impl_hint,
        p.last_used_ts,
        p.use_count
      );
    return result.changes > 0;
  } catch {
    return false;
  }
}

export function getRecentPrinciplesByProject(
  db: Database.Database,
  projectID: string,
  limit: number = 10
): Principle[] {
  const likeUnix = projectID.replace(/-/g, "/") + "%";
  const likeWindows = projectID.replace(/-/g, "\\") + "%";
  return db
    .prepare(
      `SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
              COALESCE(concepts,'') as concepts, COALESCE(files_modified,'') as files_modified,
              COALESCE(status,'') as status, COALESCE(conflict_peer_id,'') as conflict_peer_id,
              COALESCE(outcome,'unknown') as outcome, COALESCE(impl_hint,'') as impl_hint,
              COALESCE(last_used_ts,'') as last_used_ts, COALESCE(use_count,0) as use_count
       FROM principles
       WHERE (project_id = ? OR project_id LIKE ? OR project_id LIKE ?)
         AND (status = 'active' OR status IS NULL OR status = '')
       ORDER BY ts DESC LIMIT ?`
    )
    .all(projectID, likeUnix, likeWindows, limit) as Principle[];
}

export function getRecentSessionSummariesByProject(
  db: Database.Database,
  projectID: string,
  limit: number = 5
): SessionSummary[] {
  const likeUnix = projectID.replace(/-/g, "/") + "%";
  const likeWindows = projectID.replace(/-/g, "\\") + "%";
  return db
    .prepare(
      `SELECT id, ts, session_id, project_id,
              COALESCE(checkpoint_kind,'') as checkpoint_kind, COALESCE(checkpoint_key,'') as checkpoint_key,
              COALESCE(summary,'') as summary, COALESCE(request,'') as request, COALESCE(investigation,'') as investigation,
              COALESCE(learnings,'') as learnings, COALESCE(next_steps,'') as next_steps, COALESCE(keywords,'') as keywords, tokens
       FROM session_summaries
       WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
       ORDER BY ts DESC LIMIT ?`
    )
    .all(projectID, likeUnix, likeWindows, limit) as SessionSummary[];
}

export function getDistillationHealth(
  db: Database.Database
): DistillationHealth | null {
  try {
    const row = db
      .prepare(
        `SELECT
          COALESCE(last_run_at,'') as last_run_at,
          COALESCE(last_success_at,'') as last_success_at,
          COALESCE(last_error_at,'') as last_error_at,
          COALESCE(last_error_message,'') as last_error_message,
          COALESCE(last_status,'pending') as last_status,
          COALESCE(consecutive_failures,0) as consecutive_failures,
          COALESCE(total_successes,0) as total_successes,
          COALESCE(total_failures,0) as total_failures,
          COALESCE(last_attempted_events,0) as last_attempted_events,
          COALESCE(last_distilled_principles,0) as last_distilled_principles,
          COALESCE(last_duration_ms,0) as last_duration_ms,
          COALESCE(next_scheduled_at,'') as next_scheduled_at
       FROM distillation_health WHERE id = 'singleton'`
      )
      .get() as DistillationHealth | undefined;
    return row || null;
  } catch {
    return null;
  }
}

export function recordDistillationSuccess(
  db: Database.Database,
  runAt: Date,
  durationMs: number,
  attemptedEvents: number,
  distilledPrinciples: number,
  nextScheduledAt: Date
): void {
  const runAtStr = runAt.toISOString();
  db.prepare(`UPDATE distillation_health SET
    last_run_at=?,
    last_success_at=?,
    last_status='success',
    consecutive_failures=0,
    total_successes=total_successes+1,
    last_attempted_events=?,
    last_distilled_principles=?,
    last_duration_ms=?,
    next_scheduled_at=?
    WHERE id='singleton'`).run(
    runAtStr,
    runAtStr,
    attemptedEvents,
    distilledPrinciples,
    durationMs,
    nextScheduledAt.toISOString()
  );
}

export function recordDistillationFailure(
  db: Database.Database,
  runAt: Date,
  durationMs: number,
  attemptedEvents: number,
  errMessage: string,
  nextScheduledAt: Date
): void {
  const runAtStr = runAt.toISOString();
  db.prepare(`UPDATE distillation_health SET
    last_run_at=?,
    last_error_at=?,
    last_error_message=?,
    last_status='failed',
    consecutive_failures=consecutive_failures+1,
    total_failures=total_failures+1,
    last_attempted_events=?,
    last_duration_ms=?,
    next_scheduled_at=?
    WHERE id='singleton'`).run(
    runAtStr,
    runAtStr,
    errMessage,
    attemptedEvents,
    durationMs,
    nextScheduledAt.toISOString()
  );
}
