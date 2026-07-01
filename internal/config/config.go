package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Config struct {
	Provider           string
	APIKey             string
	Model              string
	BaseURL            string
	Context7APIKey     string
	ExaAPIKey          string
	TavilyAPIKey       string
	Timeout            string
	Retries            int
	DistillInterval    string
	OllamaTimeout      string
	OllamaStartupWait  string
}

func Path() string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".forge", "config")
}

func Load() (Config, error) {
	var cfg Config

	// 1. Load global config
	globalPath := Path()
	if data, err := os.ReadFile(globalPath); err == nil {
		parseConfigData(data, &cfg)
	} else if !os.IsNotExist(err) {
		return Config{}, err
	}

	// 2. Load project local config if in a git repo
	if root := findGitRoot(); root != "" {
		// Try .forge/config (committed project config)
		localPath := filepath.Join(root, ".forge", "config")
		if data, err := os.ReadFile(localPath); err == nil {
			parseConfigData(data, &cfg)
		}
		// Try .forge/config.local (gitignored user overrides)
		localPrivatePath := filepath.Join(root, ".forge", "config.local")
		if data, err := os.ReadFile(localPrivatePath); err == nil {
			parseConfigData(data, &cfg)
		}
	}

	return cfg, nil
}

func parseConfigData(data []byte, cfg *Config) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "FORGE_PROVIDER":
			cfg.Provider = value
		case "FORGE_API_KEY":
			cfg.APIKey = value
		case "FORGE_MODEL":
			cfg.Model = value
		case "FORGE_BASE_URL", "FORGE_API_URL":
			cfg.BaseURL = value
		case "CONTEXT7_API_KEY":
			cfg.Context7APIKey = value
		case "EXA_API_KEY", "FORGE_EXA_API_KEY":
			cfg.ExaAPIKey = value
		case "TAVILY_API_KEY", "FORGE_TAVILY_API_KEY":
			cfg.TavilyAPIKey = value
		case "FORGE_TIMEOUT":
			cfg.Timeout = value
		case "FORGE_RETRIES":
			fmt.Sscanf(value, "%d", &cfg.Retries)
		case "FORGE_DISTILL_INTERVAL":
			cfg.DistillInterval = value
		case "FORGE_OLLAMA_TIMEOUT":
			cfg.OllamaTimeout = value
		case "FORGE_OLLAMA_STARTUP_WAIT":
			cfg.OllamaStartupWait = value
		}
	}
}

func findGitRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}


func Save(cfg Config) error {
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var lines []string
	if cfg.Provider != "" {
		lines = append(lines, fmt.Sprintf("FORGE_PROVIDER=%s", cfg.Provider))
	}
	if cfg.APIKey != "" {
		lines = append(lines, fmt.Sprintf("FORGE_API_KEY=%s", cfg.APIKey))
	}
	if cfg.Model != "" {
		lines = append(lines, fmt.Sprintf("FORGE_MODEL=%s", cfg.Model))
	}
	if cfg.BaseURL != "" {
		lines = append(lines, fmt.Sprintf("FORGE_BASE_URL=%s", cfg.BaseURL))
	}
	if cfg.Context7APIKey != "" {
		lines = append(lines, fmt.Sprintf("CONTEXT7_API_KEY=%s", cfg.Context7APIKey))
	}
	if cfg.ExaAPIKey != "" {
		lines = append(lines, fmt.Sprintf("EXA_API_KEY=%s", cfg.ExaAPIKey))
	}
	if cfg.TavilyAPIKey != "" {
		lines = append(lines, fmt.Sprintf("TAVILY_API_KEY=%s", cfg.TavilyAPIKey))
	}
	if cfg.Timeout != "" {
		lines = append(lines, fmt.Sprintf("FORGE_TIMEOUT=%s", cfg.Timeout))
	}
	if cfg.Retries > 0 {
		lines = append(lines, fmt.Sprintf("FORGE_RETRIES=%d", cfg.Retries))
	}
	if cfg.DistillInterval != "" {
		lines = append(lines, fmt.Sprintf("FORGE_DISTILL_INTERVAL=%s", cfg.DistillInterval))
	}
	if cfg.OllamaTimeout != "" {
		lines = append(lines, fmt.Sprintf("FORGE_OLLAMA_TIMEOUT=%s", cfg.OllamaTimeout))
	}
	if cfg.OllamaStartupWait != "" {
		lines = append(lines, fmt.Sprintf("FORGE_OLLAMA_STARTUP_WAIT=%s", cfg.OllamaStartupWait))
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func SetEnvFromFile() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	if cfg.Provider != "" {
		os.Setenv("FORGE_PROVIDER", cfg.Provider)
	}
	if cfg.APIKey != "" {
		os.Setenv("FORGE_API_KEY", cfg.APIKey)
	}
	if cfg.Model != "" {
		os.Setenv("FORGE_MODEL", cfg.Model)
	}
	if cfg.BaseURL != "" {
		os.Setenv("FORGE_BASE_URL", cfg.BaseURL)
		os.Setenv("FORGE_API_URL", cfg.BaseURL) // legacy compatibility for older paths
	}
	if cfg.Context7APIKey != "" {
		os.Setenv("CONTEXT7_API_KEY", cfg.Context7APIKey)
	}
	if cfg.ExaAPIKey != "" {
		os.Setenv("EXA_API_KEY", cfg.ExaAPIKey)
	}
	if cfg.TavilyAPIKey != "" {
		os.Setenv("TAVILY_API_KEY", cfg.TavilyAPIKey)
	}
	if cfg.Timeout != "" {
		os.Setenv("FORGE_TIMEOUT", cfg.Timeout)
	}
	if cfg.Retries > 0 {
		os.Setenv("FORGE_RETRIES", fmt.Sprintf("%d", cfg.Retries))
	}
	if cfg.DistillInterval != "" {
		os.Setenv("FORGE_DISTILL_INTERVAL", cfg.DistillInterval)
	}
	if cfg.OllamaTimeout != "" {
		os.Setenv("FORGE_OLLAMA_TIMEOUT", cfg.OllamaTimeout)
	}
	if cfg.OllamaStartupWait != "" {
		os.Setenv("FORGE_OLLAMA_STARTUP_WAIT", cfg.OllamaStartupWait)
	}
	return nil
}

// CheckProviderModelCompat returns a descriptive error when the model or API key
// obviously don't belong to the chosen provider. It catches typos and common
// confusion (e.g. using a ChatGPT or Gemini model name with --provider ollama) before
// any network round-trip, so the user/agent gets an actionable message immediately.
//
// Custom-proxy users who set a non-default --base-url bypass the model-name
// checks for openai/groq since those APIs are widely cloned with different
// model catalogues. Anthropic and Ollama have provider-specific API formats
// that are never model-catalogue agnostic, so the checks always apply there.
func CheckProviderModelCompat(provider, model, apiKey, baseURL string) error {
	switch provider {
	case "anthropic":
		if model != "" && !strings.HasPrefix(model, "claude-") {
			return fmt.Errorf("model %q does not belong to provider %q — Anthropic only serves Claude models (e.g. claude-haiku-4-5-20251001, claude-sonnet-4-6)", model, provider)
		}
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (sk-ant-...) — get one at console.anthropic.com", provider)
		}
	case "forgememo":
		if model != "" && !strings.HasPrefix(model, "claude-") {
			return fmt.Errorf("model %q does not belong to provider %q — Forgememo uses Claude models (e.g. claude-haiku-4-5-20251001)", model, provider)
		}
	case "openai":
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (sk-...) — get one at platform.openai.com\nNote: ChatGPT Plus/Pro subscription does not include API access; API billing is separate", provider)
		}
		// Skip model-name check when a custom base URL is set: many OpenAI-compatible
		// proxies (Azure, Together AI, etc.) serve non-gpt models under this API format.
		defaultBase := baseURL == "" || baseURL == "https://api.openai.com" || baseURL == "https://api.openai.com/"
		if defaultBase && model != "" && (strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "llama") || strings.Contains(model, "gemini")) {
			return fmt.Errorf("model %q does not belong to provider %q — OpenAI serves gpt-*, o1, o3, and similar models", model, provider)
		}
	case "groq":
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (gsk-...) — get one at console.groq.com", provider)
		}
		defaultBase := baseURL == "" || strings.HasPrefix(baseURL, "https://api.groq.com")
		if defaultBase && model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-") || strings.Contains(model, "gemini")) {
			return fmt.Errorf("model %q does not belong to provider %q — Groq serves open-source models (llama-3.3-70b-versatile, gemma2-9b-it, etc.)\nSee https://console.groq.com/docs/models for the full list", model, provider)
		}
	case "nvidia":
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (nvapi-...) — get one at build.nvidia.com", provider)
		}
		defaultBase := baseURL == "" || strings.HasPrefix(baseURL, "https://integrate.api.nvidia.com")
		if defaultBase && model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-") || strings.Contains(model, "gemini")) {
			return fmt.Errorf("model %q does not belong to provider %q — NVIDIA serves open-source and proprietary models (llama-3.3-70b-instruct, nemotron-340b, etc.)\nSee https://build.nvidia.com for the full list", model, provider)
		}
	case "ollama":
		if model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-") || strings.Contains(model, "gemini")) {
			return fmt.Errorf("model %q is not an Ollama model — Ollama serves locally installed open-source models (e.g. llama3:latest, gemma2:9b)\nRun 'ollama list' to see installed models, or 'ollama pull <name>' to install one", model)
		}
	case "antigravity":
		if model != "" && model != "flash" && model != "flash_lite" && model != "pro" {
			return fmt.Errorf("model %q does not belong to provider %q — Antigravity only supports: flash, flash_lite, pro", model, provider)
		}
	}
	return nil
}
