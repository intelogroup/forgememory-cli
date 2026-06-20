import { spawn } from "node:child_process";
import { once } from "node:events";

export class MCPClient {
  private process: import("child_process").ChildProcess;
  private stdin: import("stream").Writable;
  private buffer: string = "";
  private pending: Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  > = new Map();
  private nextID = 1;
  private _closed = false;

  constructor(forgeBinary: string) {
    this.process = spawn(forgeBinary, ["mcp"], {
      stdio: ["pipe", "pipe", "inherit"],
    });

    this.stdin = this.process.stdin!;
    const stdout = this.process.stdout!;

    stdout.on("data", (chunk: Buffer) => {
      this.buffer += chunk.toString();
      this.tryParseMessages();
    });

    this.process.on("exit", () => {
      this._closed = true;
      for (const [, entry] of this.pending) {
        entry.reject(new Error("MCP process exited"));
      }
      this.pending.clear();
    });

    this.process.on("error", () => {
      this._closed = true;
    });
  }

  private tryParseMessages(): void {
    while (true) {
      const idx = this.buffer.indexOf("\n");
      if (idx < 0) break;
      const line = this.buffer.slice(0, idx).trim();
      this.buffer = this.buffer.slice(idx + 1);
      if (!line) continue;
      try {
        const msg = JSON.parse(line);
        const entry = this.pending.get(msg.id);
        if (entry) {
          this.pending.delete(msg.id);
          if (msg.error) {
            entry.reject(new Error(msg.error.message));
          } else {
            entry.resolve(msg.result);
          }
        }
      } catch {
        // ignore malformed lines
      }
    }
  }

  async initialize(): Promise<void> {
    await this.sendRequest("initialize", {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "forge-mastra", version: "0.1.0" },
    });
  }

  private sendRequest(
    method: string,
    params?: Record<string, unknown>
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const id = this.nextID++;
      const req: Record<string, unknown> = { jsonrpc: "2.0", id, method };
      if (params) req.params = params;
      this.pending.set(id, { resolve, reject });
      this.stdin.write(JSON.stringify(req) + "\n");
    });
  }

  async callTool(
    name: string,
    args: Record<string, unknown>,
    timeoutMs: number = 5000
  ): Promise<string> {
    const result: unknown = await this.sendRequest("tools/call", {
      name,
      arguments: args,
    });
    const toolResp = result as {
      content?: Array<{ type: string; text: string }>;
    };
    if (!toolResp?.content) return "";
    return toolResp.content
      .filter((c) => c.text)
      .map((c) => c.text)
      .join("\n");
  }

  async close(): Promise<void> {
    if (this._closed) return;
    this._closed = true;
    try {
      this.process.stdin?.end();
    } catch {}
    this.process.kill("SIGTERM");
    const exited = once(this.process, "exit");
    const timeout = setTimeout(() => {
      try { this.process.kill("SIGKILL"); } catch {}
    }, 5000);
    await exited;
    clearTimeout(timeout);
  }
}

export async function gatherMCPContext(
  forgeBinary: string,
  projectID: string
): Promise<string | null> {
  if (!projectID) return null;

  let client: MCPClient | null = null;
  try {
    client = new MCPClient(forgeBinary);
    await client.initialize();
  } catch {
    return null;
  }

  try {
    const lines: string[] = [];
    lines.push("[EXISTING KNOWLEDGE — what Forge already knows about this project]\n");

    const principles = await client.callTool("get_principles", {
      project_id: projectID,
      limit: 10,
    });
    if (principles) {
      lines.push("Existing principles:");
      lines.push(formatMCPText(principles));
    }

    const summaries = await client.callTool("get_session_summaries", {
      project_id: projectID,
      limit: 5,
    });
    if (summaries) {
      lines.push("Past sessions:");
      lines.push(formatMCPText(summaries));
    }

    const patterns = await client.callTool("get_cross_session_patterns", {
      project_id: projectID,
      limit: 10,
    });
    if (patterns) {
      lines.push("Cross-session patterns:");
      lines.push(formatMCPText(patterns));
    }

    const result = lines.filter(Boolean).join("\n").trim();
    if (!result) return null;

    return (
      result +
      "\n\n[END EXISTING KNOWLEDGE]\n" +
      "\nThe block above shows what we already know about this project. Use it to avoid duplicating existing knowledge. The new session events follow below.\n"
    );
  } finally {
    await client.close();
  }
}

function formatMCPText(text: string): string {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => {
      if (!l) return false;
      if (l.startsWith("#")) return false;
      if (l.startsWith("---") || l.startsWith("___")) return false;
      if (l.startsWith("*")) return false;
      return true;
    })
    .map((l) => "  " + l)
    .join("\n");
}
