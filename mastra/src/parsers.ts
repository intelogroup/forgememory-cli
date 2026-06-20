import { SessionSummaryResult, PrincipleResult, ChunkSummary } from "./prompts.js";

function extractJSON<T>(response: string): string {
  response = response.trim();
  const start = response.indexOf("{");
  const end = response.lastIndexOf("}");
  if (start >= 0 && end > start) {
    return response.slice(start, end + 1);
  }
  return response;
}

function extractJSONArray<T>(response: string): string {
  response = response.trim();
  const start = response.indexOf("[");
  const end = response.lastIndexOf("]");
  if (start >= 0 && end > start) {
    return response.slice(start, end + 1);
  }
  return response;
}

export function parseSessionSummary(response: string): SessionSummaryResult {
  const json = extractJSON(response);
  const raw = JSON.parse(json);
  return {
    request: raw.request || "",
    investigation: raw.investigation || "",
    learnings: raw.learnings || "",
    next_steps: raw.next_steps || "",
    keywords: Array.isArray(raw.keywords) ? raw.keywords : [],
  };
}

export function parseChunkSummary(response: string): ChunkSummary {
  const json = extractJSON(response);
  const raw = JSON.parse(json);
  return {
    index: 0,
    events: 0,
    request: raw.request || "",
    investigation: raw.investigation || "",
    learnings: raw.learnings || "",
    nextSteps: raw.next_steps || "",
    keywords: Array.isArray(raw.keywords) ? raw.keywords : [],
  };
}

const GENERIC_PATTERNS = [
  /\b(curl|grep|rg|ripgrep|jsonl|transcript path|session id|tool call|tool metadata|log files?|debugging workflow|searching)\b/i,
];

const GENERIC_PHRASES = [
  "use curl",
  "use grep",
  "use rg",
  "grep is useful",
  "logs help debugging",
  "log files help",
  "search the codebase",
  "inspect transcript",
  "session ids help",
];

export function shouldKeepPrinciple(p: {
  title: string;
  narrative: string;
  type: string;
}): boolean {
  if (!p.title || !p.narrative || !p.type) return false;
  const text = `${p.title} ${p.narrative}`.toLowerCase().trim();
  if (!text) return false;
  for (const pattern of GENERIC_PATTERNS) {
    if (pattern.test(text)) return false;
  }
  for (const phrase of GENERIC_PHRASES) {
    if (text.includes(phrase)) return false;
  }
  return true;
}

export function parsePrinciples(response: string): PrincipleResult[] {
  const json = extractJSONArray(response);
  const raw: PrincipleResult[] = JSON.parse(json);
  if (!Array.isArray(raw)) return [];

  return raw
    .map((r) => ({
      type: (r.type || "").trim(),
      title: (r.title || "").trim(),
      narrative: (r.narrative || "").trim(),
      impact_score:
        r.impact_score < 0 ? 0 : r.impact_score > 1 ? 1 : r.impact_score || 0,
      outcome: ["success", "failure"].includes((r.outcome || "").trim())
        ? (r.outcome as string).trim()
        : "unknown",
      impl_hint: (r.impl_hint || "").trim().slice(0, 120),
      concepts: Array.isArray(r.concepts) ? r.concepts : [],
      files_modified: Array.isArray(r.files_modified) ? r.files_modified : [],
    }))
    .filter(shouldKeepPrinciple);
}
