import fs from "node:fs";
import path from "node:path";
import os from "node:os";

export interface ProviderConfig {
  provider: string;
  apiKey: string;
  model: string;
  baseURL: string;
  timeout: number;
  retries: number;
}

function readConfigFile(): Record<string, string> {
  try {
    const cfgPath = path.join(os.homedir(), ".forge", "config");
    const content = fs.readFileSync(cfgPath, "utf-8");
    const cfg: Record<string, string> = {};
    for (const line of content.split("\n")) {
      const eq = line.indexOf("=");
      if (eq > 0) {
        cfg[line.slice(0, eq)] = line.slice(eq + 1);
      }
    }
    return cfg;
  } catch {
    return {};
  }
}

export function loadConfig(): ProviderConfig {
  const env = process.env;
  const fileCfg = readConfigFile();
  const getVal = (envKey: string, fileKey: string): string =>
    env[envKey] || fileCfg[fileKey] || "";

  let provider = getVal("FORGE_PROVIDER", "FORGE_PROVIDER");
  let apiKey = getVal("FORGE_API_KEY", "FORGE_API_KEY");
  let model = getVal("FORGE_MODEL", "FORGE_MODEL");
  let baseURL = getVal("FORGE_BASE_URL", "FORGE_BASE_URL");
  let timeout = parseInt(getVal("FORGE_TIMEOUT", "FORGE_TIMEOUT"), 10) || 30000;
  let retries = parseInt(getVal("FORGE_RETRIES", "FORGE_RETRIES"), 10) || 3;

  if (!baseURL) {
    baseURL = env["FORGE_API_URL"] || "";
  }

  if (!provider || !apiKey) {
    if (!provider && fileCfg["FORGE_PROVIDER"]) {
      provider = fileCfg["FORGE_PROVIDER"];
    }
    if (!apiKey && fileCfg["FORGE_API_KEY"]) {
      apiKey = fileCfg["FORGE_API_KEY"];
    }
    if (!model && fileCfg["FORGE_MODEL"]) {
      model = fileCfg["FORGE_MODEL"];
    }
    if (!baseURL && fileCfg["FORGE_BASE_URL"]) {
      baseURL = fileCfg["FORGE_BASE_URL"];
    }
  }

  if (provider === "forge") provider = "forgememo";

  if (!model) {
    const defaults: Record<string, string> = {
      ollama: "llama3:latest",
      openai: "gpt-4o",
      anthropic: "claude-haiku-4-5-20251001",
      codex: "",
      forgememo: "claude-haiku-4-5-20251001",
      groq: "llama-3.3-70b-versatile",
      nvidia: "meta/llama-3.3-70b-instruct",
      antigravity: "flash",
    };
    model = defaults[provider] || "";
  }

  if (!baseURL) {
    const urls: Record<string, string> = {
      ollama: "http://localhost:11434",
      openai: "https://api.openai.com",
      anthropic: "https://api.anthropic.com",
      codex: "",
      forgememo: "https://forgememo-server.onrender.com",
      groq: "https://api.groq.com/openai",
      nvidia: "https://integrate.api.nvidia.com/v1",
      antigravity: "",
    };
    baseURL = urls[provider] || "";
  }

  if (!apiKey) {
    if (provider === "groq") apiKey = env["GROQ_API_KEY"] || "";
    if (provider === "nvidia") apiKey = env["NVIDIA_API_KEY"] || "";
  }

  if (!provider) provider = "forgememo";
  if (timeout <= 0) timeout = 30000;
  if (retries < 0) retries = 0;

  return { provider, apiKey, model, baseURL, timeout, retries };
}
