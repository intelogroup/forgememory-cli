import { Agent } from "@mastra/core/agent";
import { createOpenAI } from "@ai-sdk/openai";
import { createAnthropic } from "@ai-sdk/anthropic";
import { createOllama } from "ollama-ai-provider";
import { ProviderConfig } from "./provider.js";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import os from "node:os";
import path from "node:path";
import fs from "node:fs";

const execFilePromise = promisify(execFile);

export async function callLLM(
  cfg: ProviderConfig,
  systemPrompt: string,
  userPrompt: string
): Promise<string> {
  const provider = cfg.provider;

  if (provider === "forgememo" || provider === "forge") {
    return callForgememo(cfg, systemPrompt + "\n\n" + userPrompt);
  }
  if (provider === "antigravity") {
    return callAntigravity(cfg, systemPrompt + "\n\n" + userPrompt);
  }
  if (provider === "codex") {
    return callCodex(cfg, systemPrompt + "\n\n" + userPrompt);
  }

  const model = createModel(cfg);
  const agent = new Agent({
    id: "forge-distill",
    name: "forge-distill",
    instructions: systemPrompt,
    model,
  });

  const response = await agent.generate(userPrompt);
  return response.text;
}

function createModel(cfg: ProviderConfig) {
  switch (cfg.provider) {
    case "anthropic": {
      const anthropicProvider = createAnthropic({
        apiKey: cfg.apiKey || undefined,
        baseURL: cfg.baseURL || undefined,
      });
      return anthropicProvider(cfg.model || "claude-haiku-4-5-20251001");
    }

    case "ollama": {
      const ollamaProvider = createOllama({
        baseURL: cfg.baseURL || undefined,
      });
      return ollamaProvider(cfg.model || "llama3:latest");
    }

    default: {
      const openaiProvider = createOpenAI({
        apiKey: cfg.apiKey || undefined,
        baseURL: cfg.baseURL || undefined,
      });
      return openaiProvider.chat(cfg.model || "gpt-4o");
    }
  }
}

async function callForgememo(cfg: ProviderConfig, prompt: string): Promise<string> {
  const url = `${cfg.baseURL.replace(/\/$/, "")}/v1/inference`;
  const response = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Authorization": `Bearer ${cfg.apiKey}`,
    },
    body: JSON.stringify({
      model: cfg.model,
      prompt: prompt,
    }),
  });
  if (!response.ok) {
    throw new Error(`Forgememo API returned status ${response.status}: ${await response.text()}`);
  }
  const resObj = await response.json() as any;
  if (resObj && resObj.success && resObj.data && typeof resObj.data.response === "string") {
    return resObj.data.response;
  }
  throw new Error(`Invalid response format from Forgememo: ${JSON.stringify(resObj)}`);
}

async function callAntigravity(cfg: ProviderConfig, prompt: string): Promise<string> {
  const home = os.homedir();
  const agentAPIPath = path.join(home, ".gemini", "antigravity", "bin", "agentapi");
  if (!fs.existsSync(agentAPIPath)) {
    throw new Error(`Antigravity agentapi binary not found at ${agentAPIPath}. Please ensure the Antigravity desktop application is installed and running.`);
  }

  const model = cfg.model || "flash";
  const { stdout } = await execFilePromise(agentAPIPath, [
    "new-conversation",
    `--model=${model}`,
    prompt,
  ]);

  const apiResp = JSON.parse(stdout.trim());
  if (apiResp.error) {
    throw new Error(`Antigravity agentapi error: ${apiResp.error}`);
  }
  const convID = apiResp.response?.newConversation?.conversationId;
  if (!convID) {
    throw new Error(`Antigravity agentapi returned empty conversation ID (stdout: ${stdout})`);
  }
  return pollAntigravityTranscript(convID, cfg.timeout);
}

async function pollAntigravityTranscript(conversationID: string, timeoutMs: number): Promise<string> {
  const home = os.homedir();
  const transcriptPath = path.join(
    home,
    ".gemini",
    "antigravity",
    "brain",
    conversationID,
    ".system_generated",
    "logs",
    "transcript.jsonl"
  );

  const timeout = timeoutMs > 0 ? timeoutMs : 45000;
  const deadline = Date.now() + timeout;

  while (Date.now() < deadline) {
    if (fs.existsSync(transcriptPath)) {
      try {
        const content = fs.readFileSync(transcriptPath, "utf-8");
        const lines = content.split("\n");
        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed) continue;
          const step = JSON.parse(trimmed);
          if (step.source === "MODEL" && step.type === "PLANNER_RESPONSE") {
            if (step.status === "DONE") {
              return step.content;
            }
            if (step.status === "ERROR") {
              throw new Error(`Antigravity agent execution failed: ${step.content}`);
            }
          }
        }
      } catch (err: any) {
        if (err.code !== "ENOENT") {
          throw err;
        }
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("timeout waiting for Antigravity response");
}

async function callCodex(cfg: ProviderConfig, prompt: string): Promise<string> {
  const cmdName = process.env["FORGE_CODEX_CMD"] || "codex";
  const envArgs = process.env["FORGE_CODEX_ARGS"] ? process.env["FORGE_CODEX_ARGS"].trim().split(/\s+/) : [];

  const outPath = path.join(os.tmpdir(), `forge-codex-output-${Math.random().toString(36).substring(7)}.txt`);

  const args = [
    ...envArgs,
    "exec",
    "--skip-git-repo-check",
    "--sandbox",
    "read-only",
    "--ephemeral",
    "-o",
    outPath,
  ];
  if (cfg.model && cfg.model.trim()) {
    args.push("-m", cfg.model.trim());
  }
  args.push("-");

  return new Promise((resolve, reject) => {
    const child = spawn(cmdName, args, { stdio: ["pipe", "ignore", "pipe"] });
    let stderr = "";

    child.stderr?.on("data", (chunk: Buffer) => {
      stderr += chunk.toString();
    });

    child.on("error", (err) => {
      try { fs.unlinkSync(outPath); } catch {}
      reject(err);
    });

    child.on("close", (code) => {
      if (code !== 0) {
        try { fs.unlinkSync(outPath); } catch {}
        const msg = stderr.trim() || `exit code ${code}`;
        return reject(new Error(`codex exec failed: ${msg}`));
      }

      try {
        if (!fs.existsSync(outPath)) {
          return reject(new Error("no response from Codex"));
        }
        const response = fs.readFileSync(outPath, "utf-8").trim();
        fs.unlinkSync(outPath);
        if (!response) {
          return reject(new Error("no response from Codex"));
        }
        resolve(response);
      } catch (err) {
        reject(err);
      }
    });

    child.stdin?.write(prompt);
    child.stdin?.end();
  });
}
