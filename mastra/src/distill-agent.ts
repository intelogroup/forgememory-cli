#!/usr/bin/env node
import { loadConfig } from "./provider.js";
import { gatherMCPContext } from "./mcp.js";
import { runPipeline } from "./pipeline.js";
import { openDB } from "./db.js";

function parseArgs(): Record<string, string> {
  const args: Record<string, string> = {};
  for (let i = 2; i < process.argv.length; i++) {
    const arg = process.argv[i];
    if (arg.startsWith("--")) {
      const eq = arg.indexOf("=");
      if (eq > 0) {
        args[arg.slice(2, eq)] = arg.slice(eq + 1);
      } else {
        args[arg.slice(2)] = process.argv[++i] || "";
      }
    }
  }
  return args;
}

async function main(): Promise<void> {
  const args = parseArgs();
  const sessionID = args["session-id"];
  const forgeBinary = args["forge-binary"];
  const projectID = args["project-id"] || "";
  const checkpointKind = args["checkpoint-kind"] || "daemon";
  const checkpointKey =
    args["checkpoint-key"] || `${sessionID}:mastra`;
  const cutoffTS = args["cutoff-ts"] || "";

  if (!sessionID) process.exit(0);

  const llmCfg = loadConfig();
  const db = openDB();

  // Gather MCP context via forge binary
  let contextBlock: string | null = null;
  if (forgeBinary && projectID) {
    contextBlock = await gatherMCPContext(forgeBinary, projectID);
  }

  try {
    await runPipeline({
      sessionID,
      projectID,
      checkpointKind,
      checkpointKey,
      cutoffTS: cutoffTS || undefined,
      contextBlock,
      db,
      llmCfg,
    });
  } finally {
    db.close();
  }
}

main().catch((err) => {
  console.error(
    `distill-agent: fatal: ${err instanceof Error ? err.message : String(err)}`
  );
  process.exit(1);
});
