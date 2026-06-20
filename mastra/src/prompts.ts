import { Event } from "./db.js";

function shortTS(ts: string): string {
  if (ts.length >= 16) return ts.slice(0, 16);
  return ts.trim() || "unknown-time";
}

function truncatePayload(s: string, max: number): string {
  if (!s) return "";
  if (s.length <= max) return s;
  return s.slice(0, max) + "...";
}

function summarizeEventPayload(e: Event): string {
  if (!e.payload) return "";
  if (e.event_type === "UserPromptSubmit") {
    const prompt = extractPromptText(e.payload);
    if (prompt) return truncatePayload(prompt, 500);
    return truncatePayload(e.payload, 500);
  }
  if (["SessionStart", "SessionEnd", "Stop", "AfterAgent", "BeforeAgent"].includes(e.event_type)) {
    return "session boundary";
  }
  if (e.event_type !== "PostToolUse") return truncatePayload(e.payload, 500);

  try {
    const raw = JSON.parse(e.payload);
    const toolInput = raw.tool_input || "";
    const toolResponse = raw.tool_response || "";

    let inputSummary = "";
    if (e.tool_name === "Bash") {
      try {
        const inp = JSON.parse(
          typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput)
        );
        if (inp.command) inputSummary = truncatePayload(inp.command, 300);
      } catch {}
    } else if (["Edit", "Write", "MultiEdit"].includes(e.tool_name)) {
      try {
        const inp = JSON.parse(
          typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput)
        );
        if (inp.file_path) inputSummary = inp.file_path;
      } catch {}
    }
    if (!inputSummary) {
      inputSummary = truncatePayload(
        typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput),
        300
      );
    }

    let respSummary = "";
    if (toolResponse) {
      try {
        const respObj = JSON.parse(
          typeof toolResponse === "string"
            ? toolResponse
            : JSON.stringify(toolResponse)
        );
        if (respObj.stdout !== undefined || respObj.stderr !== undefined) {
          const combined = [respObj.stdout || "", respObj.stderr || ""]
            .filter(Boolean)
            .join("\n");
          respSummary = `exit=${respObj.exit_code || 0} ${truncatePayload(combined, 400)}`;
        }
      } catch {
        respSummary = truncatePayload(
          typeof toolResponse === "string" ? toolResponse : JSON.stringify(toolResponse),
          400
        );
      }
    }

    if (respSummary) return `input: ${inputSummary} → ${respSummary}`;
    return `input: ${inputSummary}`;
  } catch {
    return truncatePayload(e.payload, 500);
  }
}

function extractPromptText(payload: string): string {
  try {
    const parsed = JSON.parse(payload);
    if (parsed.prompt) return parsed.prompt;
    if (parsed.text) return parsed.text;
    if (parsed.content) return typeof parsed.content === "string" ? parsed.content : "";
  } catch {}
  return "";
}

export function buildSessionPrompt(
  events: Event[],
  contextBlock?: string
): string {
  let prompt = "";
  if (contextBlock) {
    prompt += contextBlock + "\n\n";
  }
  prompt +=
    "Analyze this coding session and produce a concise summary. Return JSON with these fields:\n";
  prompt +=
    "- request: What was the user trying to accomplish? (1 sentence)\n";
  prompt +=
    "- investigation: What was explored or debugged? (1-2 sentences)\n";
  prompt +=
    "- learnings: Key findings or solutions discovered (1-2 sentences)\n";
  prompt +=
    "- next_steps: Recommended follow-up actions (1 sentence, or empty string)\n";
  prompt +=
    '- keywords: Array of 3-5 key technical terms, libraries, frameworks, or concepts from this session (e.g. ["sqlite", "fts5", "go", "migration"]). These will be used to recall this session later when relevant topics come up.\n\n';
  prompt += "Session events:\n";
  for (let i = 0; i < events.length; i++) {
    const e = events[i];
    prompt += `${i + 1}. [${shortTS(e.ts)}] ${e.event_type} (${e.tool_name}): ${summarizeEventPayload(e)}\n`;
  }
  prompt += "\nReturn ONLY the JSON object, no other text.\n";
  return prompt;
}

export function buildCheckpointPrinciplesPrompt(
  request: string,
  investigation: string,
  learnings: string,
  nextSteps: string,
  keywords: string,
  contextBlock?: string
): string {
  let prompt = "";
  if (contextBlock) {
    prompt += contextBlock + "\n\n";
  }
  prompt +=
    "You are a senior staff engineer writing personal reference notes for your future self. Extract only durable, project-specific engineering principles from this synthesized checkpoint — notes worth consulting before touching this area again.\n\n";
  prompt += "Return a JSON array. Each element must have:\n";
  prompt += "- type: architecture/bugfix/pattern/preference\n";
  prompt += "- title: short description (max 80 chars)\n";
  prompt +=
    "- narrative: 2-4 sentences. Sentence 1: state the tech stack and situation where this applies (e.g. \"In a Next.js app using Convex + WorkOS...\", \"When deploying a Go service to Render behind a proxy...\"). Sentences 2-4: name the specific function, flag, config key, or data structure involved and explain WHY it matters. Write in first-person imperative. BAD: \"Caching was improved.\" GOOD: \"In a Next.js app with a reverse-proxy CDN. Always set Cache-Control: no-store on /auth endpoints via middleware/auth.go — the proxy caches 302 redirects and replays stale tokens to different users without it.\"\n";
  prompt += "- impact_score: 0.0-1.0\n";
  prompt += "- outcome: \"success\"/\"failure\"/\"unknown\"\n";
  prompt +=
    "- impl_hint: exact function, flag, or pattern used (max 120 chars, or empty)\n";
  prompt +=
    "- concepts: array from [security, pattern, gotcha, performance, trade-off, how-it-works]\n";
  prompt +=
    "- files_modified: array of file paths (empty if none)\n\n";
  prompt +=
    "Session summary:\n";
  prompt += `Goal: ${request}\n`;
  prompt += `What was done: ${investigation}\n`;
  prompt += `Key learnings: ${learnings}\n`;
  prompt += `Next steps: ${nextSteps}\n`;
  prompt += `Keywords: ${keywords}\n\n`;
  prompt +=
    "Only emit a principle for concrete, reusable techniques. Return [] if the session was routine.\n";
  prompt += "Return ONLY the JSON array, no other text.\n";
  return prompt;
}

export function buildChunkSummaryPrompt(
  events: Event[],
  chunkNum: number,
  totalChunks: number
): string {
  let prompt = `This is chunk ${chunkNum} of ${totalChunks} from a coding session. Analyze these events and produce a concise summary.\n\n`;
  prompt += "Return JSON with:\n";
  prompt += "- request: What was the user trying to accomplish in this chunk? (1 sentence)\n";
  prompt += "- investigation: What was explored or debugged? (1-2 sentences)\n";
  prompt += "- learnings: Key findings or solutions discovered (1-2 sentences)\n";
  prompt += "- next_steps: Recommended next actions (1 sentence, or empty string)\n";
  prompt += "- keywords: Array of 3-5 key technical terms from this chunk\n\n";
  prompt += "Events:\n";
  for (let i = 0; i < events.length; i++) {
    const e = events[i];
    prompt += `${i + 1}. [${shortTS(e.ts)}] ${e.event_type} (${e.tool_name}): ${summarizeEventPayload(e)}\n`;
  }
  prompt += "\nReturn ONLY the JSON object, no other text.\n";
  return prompt;
}

export function buildMasterSummaryPrompt(
  chunks: ChunkSummary[]
): string {
  let prompt =
    "Below are summaries of sequential chunks from a single coding session. Synthesize them into a coherent session-level summary.\n\n";
  for (const cs of chunks) {
    prompt += `--- Chunk ${cs.index + 1} (${cs.events} events) ---\n`;
    prompt += `Request: ${cs.request}\n`;
    prompt += `Investigation: ${cs.investigation}\n`;
    prompt += `Learnings: ${cs.learnings}\n`;
    prompt += `Next steps: ${cs.nextSteps}\n`;
    if (cs.keywords.length > 0) {
      prompt += `Keywords: ${cs.keywords.join(", ")}\n`;
    }
    prompt += "\n";
  }
  prompt +=
    "Now produce the combined session summary. Return JSON with:\n";
  prompt += "- request: What was the overall goal of the session? (1 sentence)\n";
  prompt += "- investigation: What was explored overall? (1-2 sentences)\n";
  prompt += "- learnings: Key overall findings (2-3 sentences)\n";
  prompt += "- next_steps: Recommended next actions (1 sentence, or empty string)\n";
  prompt += "- keywords: Array of 3-7 key technical terms for the full session\n";
  prompt += "\nReturn ONLY the JSON object, no other text.\n";
  return prompt;
}

export function buildChunkPrinciplesPrompt(
  final: ChunkSummary,
  chunks: ChunkSummary[]
): string {
  let prompt =
    "You are a senior staff engineer writing personal reference notes for your future self. Extract only durable, project-specific engineering principles from this coding session.\n\n";
  prompt += "Session summary:\n";
  prompt += `Goal: ${final.request}\n`;
  prompt += `What was done: ${final.investigation}\n`;
  prompt += `Key learnings: ${final.learnings}\n`;
  prompt += `Keywords: ${final.keywords.join(", ")}\n\n`;
  prompt += "Chunk breakdown:\n";
  for (const cs of chunks) {
    prompt += `  Chunk ${cs.index + 1}: ${cs.request} | ${cs.learnings}\n`;
  }
  prompt +=
    "\nReturn a JSON array. Each element must have:\n";
  prompt += "- type: architecture/bugfix/pattern/preference\n";
  prompt += "- title: short description (max 80 chars)\n";
  prompt +=
    "- narrative: 2-4 sentences explaining the technique and why it matters\n";
  prompt += "- impact_score: 0.0-1.0\n";
  prompt += "- outcome: \"success\"/\"failure\"/\"unknown\"\n";
  prompt +=
    "- impl_hint: exact function, flag, or pattern used (max 120 chars, or empty)\n";
  prompt +=
    "- concepts: array from [security, pattern, gotcha, performance, trade-off, how-it-works]\n";
  prompt +=
    "- files_modified: array of file paths (empty if none)\n\n";
  prompt +=
    "Only emit a principle for concrete, reusable techniques. Return [] if the session was routine.\n";
  prompt += "Return ONLY the JSON array, no other text.\n";
  return prompt;
}

export function buildCrossSessionPrompt(
  summaries: string
): string {
  return (
    "You are analyzing patterns across multiple coding sessions for the same project. Below are session summaries.\n\n" +
    summaries +
    "\n\nIdentify recurring patterns, persistent issues, or consistent approaches across these sessions. " +
    "Return a JSON array. Each element must have:\n" +
    "- pattern: description of the recurring theme (1-2 sentences)\n" +
    "- pattern_type: \"workflow\"/\"architecture\"/\"pain-point\"/\"approach\"\n" +
    "- evidence_session_ids: array of session IDs that exhibit this pattern (as strings)\n" +
    "- confidence: 0.0-1.0\n" +
    "\nReturn ONLY the JSON array, no other text.\n"
  );
}

export interface ChunkSummary {
  index: number;
  events: number;
  request: string;
  investigation: string;
  learnings: string;
  nextSteps: string;
  keywords: string[];
}

export interface SessionSummaryResult {
  request: string;
  investigation: string;
  learnings: string;
  next_steps: string;
  keywords: string[];
}

export interface PrincipleResult {
  type: string;
  title: string;
  narrative: string;
  impact_score: number;
  outcome: string;
  impl_hint: string;
  concepts: string[];
  files_modified: string[];
}
