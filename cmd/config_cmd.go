package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/distill"
)

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	showFlag := fs.Bool("show", false, "Show current config")
	providerFlag := fs.String("provider", "", "Provider: forgememo/anthropic/openai/groq/nvidia/ollama")
	apiKeyFlag := fs.String("api-key", "", "API key for provider")
	modelFlag := fs.String("model", "", "Model name (optional, defaults vary by provider)")
	baseURLFlag := fs.String("base-url", "", "Base URL for API (optional)")
	context7APIKeyFlag := fs.String("context7-api-key", "", "Context7 API key for official docs")
	timeoutFlag := fs.String("timeout", "", "Inference timeout (e.g. 30s)")
	retriesFlag := fs.Int("retries", -1, "Retry attempts for transient failures")
	intervalFlag := fs.String("interval", "", "Distillation interval (e.g. 10m)")
	ollamaTimeoutFlag := fs.String("ollama-timeout", "", "Ollama request timeout (e.g. 120s); overrides --timeout for Ollama")
	ollamaStartupWaitFlag := fs.String("ollama-startup-wait", "", "Grace period before first Ollama distill (e.g. 15s)")
	jsonFlag := fs.Bool("json", false, "Machine-readable JSON output")
	validateFlag := fs.Bool("validate", false, "Validate provider credentials/model access (now default; kept for compatibility)")
	noValidateFlag := fs.Bool("no-validate", false, "Skip credential validation after saving (offline scripts)")
	interactiveFlag := fs.Bool("interactive", false, "Interactive setup")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *showFlag {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		if cfg.Provider == "" {
			fmt.Println("No config set. Run 'forge config --provider forgememo' or configure another provider.")
			return
		}
		if *jsonFlag {
			out := map[string]any{
				"schema_version": "1",
				"provider":       cfg.Provider,
				"api_key_set":    cfg.APIKey != "",
				"api_key_masked": maskKey(cfg.APIKey),
				"model":          cfg.Model,
				"base_url":       cfg.BaseURL,
				"timeout":        cfg.Timeout,
				"retries":        cfg.Retries,
				"interval":       cfg.DistillInterval,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("Provider:  %s\n", cfg.Provider)
		if cfg.APIKey != "" {
			fmt.Printf("API Key:    %s\n", maskKey(cfg.APIKey))
		}
		if cfg.Model != "" {
			fmt.Printf("Model:      %s\n", cfg.Model)
		}
		if cfg.BaseURL != "" {
			fmt.Printf("Base URL:   %s\n", cfg.BaseURL)
		}
		if cfg.Timeout != "" {
			fmt.Printf("Timeout:    %s\n", cfg.Timeout)
		}
		if cfg.Retries > 0 {
			fmt.Printf("Retries:    %d\n", cfg.Retries)
		}
		if cfg.DistillInterval != "" {
			fmt.Printf("Interval:   %s\n", cfg.DistillInterval)
		}
		if cfg.OllamaTimeout != "" {
			fmt.Printf("Ollama timeout:       %s\n", cfg.OllamaTimeout)
		}
		if cfg.OllamaStartupWait != "" {
			fmt.Printf("Ollama startup wait:  %s\n", cfg.OllamaStartupWait)
		}
		return
	}

	interactive := *interactiveFlag || *providerFlag == ""
	if interactive {
		runConfigInteractive(providerFlag, apiKeyFlag, modelFlag, timeoutFlag, retriesFlag, intervalFlag)
	}

	if *providerFlag == "" {
		fmt.Println("Usage: forge config [options]")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --show           Show current configuration")
		fmt.Println("  --provider       Provider: forgememo, anthropic, openai, groq, nvidia, ollama, codex")
		fmt.Println("  --api-key        API key for the provider")
		fmt.Println("  --model          Model name (optional)")
		fmt.Println("  --base-url       Base URL for API (optional)")
		fmt.Println("  --context7-api-key Context7 API key for official docs")
		fmt.Println("  --timeout              Inference timeout (e.g. 30s)")
		fmt.Println("  --retries              Retry attempts for transient failures")
		fmt.Println("  --interval             Distillation interval (e.g. 10m)")
		fmt.Println("  --ollama-timeout       Ollama request timeout (e.g. 120s); overrides --timeout for Ollama")
		fmt.Println("  --ollama-startup-wait  Grace period before first Ollama distill (e.g. 15s)")
		fmt.Println("  --validate       Force credential validation (default when --provider/--api-key/--model/--base-url changes)")
		fmt.Println("  --no-validate    Skip credential validation (for offline scripts)")
		fmt.Println("  --json           JSON output (with --show)")
		fmt.Println("  --interactive    Interactive setup")
		fmt.Println("")
		fmt.Println("Defaults by provider:")
		fmt.Println("  forgememo: claude-haiku-4-5-20251001")
		fmt.Println("  anthropic: claude-haiku-4-5-20251001")
		fmt.Println("  openai:    gpt-4o")
		fmt.Println("  groq:      llama-3.3-70b-versatile (uses GROQ_API_KEY env or --api-key)")
		fmt.Println("  nvidia:    meta/llama-3.3-70b-instruct (uses NVIDIA_API_KEY env or --api-key)")
		fmt.Println("  ollama:    llama3:latest")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  forge config --show")
		fmt.Println("  forge config --provider forgememo")
		fmt.Println("  forge config --provider openai --api-key sk-...")
		fmt.Println("  forge config --provider nvidia --api-key nvapi-...")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-...")
		fmt.Println("  forge config --provider groq --api-key gsk-...")
		fmt.Println("  forge config --provider ollama --model llama3:latest")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-... --validate")
		fmt.Println("  forge config --provider openai --context7-api-key ctx7sk-...")
		os.Exit(0)
	}

	validProviders := map[string]bool{"forgememo": true, "forge": true, "anthropic": true, "openai": true, "ollama": true, "groq": true, "nvidia": true, "codex": true}
	if !validProviders[*providerFlag] {
		fmt.Fprintf(os.Stderr, "Error: provider must be one of: forgememo, anthropic, openai, groq, nvidia, ollama, codex\n")
		os.Exit(1)
	}
	if *providerFlag == "forge" {
		*providerFlag = "forgememo"
	}

	if *timeoutFlag != "" {
		if _, err := time.ParseDuration(*timeoutFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --timeout value %q\n", *timeoutFlag)
			os.Exit(1)
		}
	}
	if *intervalFlag != "" {
		if _, err := time.ParseDuration(*intervalFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --interval value %q\n", *intervalFlag)
			os.Exit(1)
		}
	}
	if *ollamaTimeoutFlag != "" {
		if _, err := time.ParseDuration(*ollamaTimeoutFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --ollama-timeout value %q\n", *ollamaTimeoutFlag)
			os.Exit(1)
		}
	}
	if *ollamaStartupWaitFlag != "" {
		if _, err := time.ParseDuration(*ollamaStartupWaitFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --ollama-startup-wait value %q\n", *ollamaStartupWaitFlag)
			os.Exit(1)
		}
	}
	if *retriesFlag < -1 {
		fmt.Fprintln(os.Stderr, "Error: --retries must be >= 0")
		os.Exit(1)
	}

	existing, _ := config.Load()
	cfg := existing
	cfg.Provider = *providerFlag
	if *apiKeyFlag != "" {
		cfg.APIKey = *apiKeyFlag
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	} else if cfg.Model == "" || cfg.Provider != existing.Provider {
		cfg.Model = defaultModelForProvider(cfg.Provider)
	}
	if *baseURLFlag != "" {
		cfg.BaseURL = *baseURLFlag
	}
	if *context7APIKeyFlag != "" {
		cfg.Context7APIKey = *context7APIKeyFlag
	}
	if *timeoutFlag != "" {
		cfg.Timeout = *timeoutFlag
	}
	if *retriesFlag >= 0 {
		cfg.Retries = *retriesFlag
	}
	if *intervalFlag != "" {
		cfg.DistillInterval = *intervalFlag
	}
	if *ollamaTimeoutFlag != "" {
		cfg.OllamaTimeout = *ollamaTimeoutFlag
	}
	if *ollamaStartupWaitFlag != "" {
		cfg.OllamaStartupWait = *ollamaStartupWaitFlag
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}

	// Catch obvious provider/model mismatches locally before touching the network.
	if err := checkProviderModelCompat(cfg.Provider, cfg.Model, cfg.APIKey, cfg.BaseURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	// Validate by default whenever credentials/model/base-url change. Skipping
	// validation used to leave the daemon failing every minute with opaque
	// "Invalid API key" errors. Allow opting out for offline scripts.
	credChanged := *providerFlag != existing.Provider ||
		(*apiKeyFlag != "" && *apiKeyFlag != existing.APIKey) ||
		(*modelFlag != "" && *modelFlag != existing.Model) ||
		(*baseURLFlag != "" && *baseURLFlag != existing.BaseURL)
	shouldValidate := *validateFlag || (credChanged && !*noValidateFlag)

	if shouldValidate {
		fmt.Println("Testing connection...")
		if err := distill.ValidateConfig(distillConfigFromUserConfig(cfg)); err != nil {
			fmt.Fprintf(os.Stderr, "Validation failed: %s\n", distill.UserMessage(err))
			fmt.Fprintln(os.Stderr, "Suggestions:")
			for i, hint := range distill.DiagnosticHints(distillConfigFromUserConfig(cfg), err) {
				fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, hint)
			}
			fmt.Fprintln(os.Stderr, "\nNot saving — config left unchanged. Use --no-validate to save without testing.")
			os.Exit(1)
		}
		fmt.Println("Validation successful.")
	}

	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	// Surface env-vs-config drift the moment the user touches config —
	// otherwise a stale shell export will silently shadow the new value
	// when the daemon next starts and the user has no idea why.
	warnEnvVsConfig()

	fmt.Printf("Configuration saved.\n")
	fmt.Printf("Provider: %s\n", cfg.Provider)
	fmt.Printf("Model: %s\n", cfg.Model)
	if cfg.APIKey != "" {
		fmt.Println("API Key: " + maskKey(cfg.APIKey))
	}
	if cfg.Context7APIKey != "" {
		fmt.Println("Context7 API Key: " + maskKey(cfg.Context7APIKey))
	}
	if cfg.Timeout != "" {
		fmt.Printf("Timeout: %s\n", cfg.Timeout)
	}
	fmt.Printf("Retries: %d\n", cfg.Retries)
	fmt.Println("\nTo apply, either:")
	fmt.Println("  1. Run: export $(cat ~/.forge/config | xargs)  # in your shell")
	fmt.Println("  2. Or add to your shell profile (~/.zshrc, ~/.bashrc)")
}

func runConfigInteractive(providerFlag, apiKeyFlag, modelFlag, timeoutFlag *string, retriesFlag *int, intervalFlag *string) {
	reader := bufio.NewReader(os.Stdin)
	isTTY := false
	if fi, err := os.Stdin.Stat(); err == nil {
		isTTY = (fi.Mode()&os.ModeCharDevice) != 0
	}
	if !isTTY {
		return
	}

	if *providerFlag == "" {
		*providerFlag = promptChoice(reader, "Select provider", []string{"forgememo", "anthropic", "openai", "groq", "nvidia", "ollama"}, "forgememo")
	}
	if *apiKeyFlag == "" && (*providerFlag == "anthropic" || *providerFlag == "openai" || *providerFlag == "groq" || *providerFlag == "nvidia") {
		*apiKeyFlag = promptInput(reader, "API Key", "")
	}
	if *modelFlag == "" {
		*modelFlag = promptModel(reader, *providerFlag)
	}
	if *timeoutFlag == "" {
		*timeoutFlag = promptInput(reader, "Timeout", "30s")
	}
	if *retriesFlag < 0 {
		retryRaw := promptInput(reader, "Retries", "3")
		if retryVal, err := strconv.Atoi(retryRaw); err == nil {
			*retriesFlag = retryVal
		}
	}
	if *intervalFlag == "" {
		*intervalFlag = promptInput(reader, "Distillation interval", "10m")
	}
}

func promptInput(reader *bufio.Reader, label, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("? %s [%s]: ", label, defaultValue)
	} else {
		fmt.Printf("? %s: ", label)
	}
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text == "" {
		return defaultValue
	}
	return text
}

func promptChoice(reader *bufio.Reader, label string, options []string, defaultValue string) string {
	fmt.Printf("? %s:\n", label)
	for _, opt := range options {
		fmt.Printf("  - %s\n", opt)
	}
	return promptInput(reader, label, defaultValue)
}

func promptModel(reader *bufio.Reader, provider string) string {
	switch provider {
	case "anthropic":
		fmt.Println("? Select model:")
		fmt.Println("  - claude-haiku-4-5-20251001 (recommended: fastest, budget-friendly)")
		fmt.Println("  - claude-sonnet-4-6 (balanced)")
		fmt.Println("  - claude-opus-4-6 (most capable, slowest)")
		return promptInput(reader, "Model", "claude-haiku-4-5-20251001")
	case "openai":
		fmt.Println("? Select model:")
		fmt.Println("  - gpt-4o (default)")
		fmt.Println("  - gpt-4-turbo")
		fmt.Println("  - gpt-3.5-turbo (budget)")
		return promptInput(reader, "Model", "gpt-4o")
	case "ollama":
		return promptInput(reader, "Model", "llama3:latest")
	case "groq":
		fmt.Println("? Select model:")
		fmt.Println("  - llama-3.3-70b-versatile (default: fast, large context)")
		fmt.Println("  - llama3-8b-8192 (budget: faster, lower TPM cost)")
		fmt.Println("  - gemma2-9b-it")
		return promptInput(reader, "Model", "llama-3.3-70b-versatile")
	case "nvidia":
		fmt.Println("? Select model:")
		fmt.Println("  - meta/llama-3.3-70b-instruct (recommended: balanced)")
		fmt.Println("  - meta/llama-3.1-405b-instruct (most capable, large context)")
		fmt.Println("  - nvidia/llama-3.1-nemotron-70b-instruct (optimized for accuracy)")
		return promptInput(reader, "Model", "meta/llama-3.3-70b-instruct")
	case "forgememo", "forge":
		return promptInput(reader, "Model", "claude-haiku-4-5-20251001")
	default:
		return defaultModelForProvider(provider)
	}
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case "forgememo", "forge":
		return "claude-haiku-4-5-20251001"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "openai":
		return "gpt-4o"
	case "groq":
		return "llama-3.3-70b-versatile"
	case "nvidia":
		return "meta/llama-3.3-70b-instruct"
	case "ollama":
		return "llama3:latest"
	default:
		return "claude-haiku-4-5-20251001"
	}
}

func distillConfigFromUserConfig(cfg config.Config) distill.Config {
	d := distill.LoadConfig()
	d.Provider = distill.Provider(cfg.Provider)
	if d.Provider == "forge" {
		d.Provider = distill.ProviderForgememo
	}
	// For groq, prefer FORGE_API_KEY from config, but fall back to GROQ_API_KEY env
	// (already populated by distill.LoadConfig via the GROQ_API_KEY fallback).
	if cfg.APIKey != "" {
		d.APIKey = cfg.APIKey
	}
	if cfg.Model != "" {
		d.Model = cfg.Model
	}
	if cfg.BaseURL != "" {
		d.BaseURL = cfg.BaseURL
	}
	if cfg.Timeout != "" {
		if timeout, err := time.ParseDuration(cfg.Timeout); err == nil {
			d.Timeout = timeout
		}
	}
	if cfg.Retries > 0 {
		d.Retries = cfg.Retries
	}
	return d
}

func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// checkProviderModelCompat returns a descriptive error when the model or API key
// obviously don't belong to the chosen provider. It catches typos and common
// confusion (e.g. using a ChatGPT model name with --provider ollama) before
// any network round-trip, so the user gets an actionable message immediately.
//
// Custom-proxy users who set a non-default --base-url bypass the model-name
// checks for openai/groq since those APIs are widely cloned with different
// model catalogues. Anthropic and Ollama have provider-specific API formats
// that are never model-catalogue agnostic, so the checks always apply there.
func checkProviderModelCompat(provider, model, apiKey, baseURL string) error {
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
		if defaultBase && model != "" && (strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "llama")) {
			return fmt.Errorf("model %q does not belong to provider %q — OpenAI serves gpt-*, o1, o3, and similar models", model, provider)
		}
	case "groq":
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (gsk-...) — get one at console.groq.com", provider)
		}
		defaultBase := baseURL == "" || strings.HasPrefix(baseURL, "https://api.groq.com")
		if defaultBase && model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-")) {
			return fmt.Errorf("model %q does not belong to provider %q — Groq serves open-source models (llama-3.3-70b-versatile, gemma2-9b-it, etc.)\nSee https://console.groq.com/docs/models for the full list", model, provider)
		}
	case "nvidia":
		if apiKey == "" {
			return fmt.Errorf("provider %q requires an API key (nvapi-...) — get one at build.nvidia.com", provider)
		}
		defaultBase := baseURL == "" || strings.HasPrefix(baseURL, "https://integrate.api.nvidia.com")
		if defaultBase && model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-")) {
			return fmt.Errorf("model %q does not belong to provider %q — NVIDIA serves open-source and proprietary models (llama-3.3-70b-instruct, nemotron-340b, etc.)\nSee https://build.nvidia.com for the full list", model, provider)
		}
	case "ollama":
		if model != "" && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "claude-")) {
			return fmt.Errorf("model %q is not an Ollama model — Ollama serves locally installed open-source models (e.g. llama3:latest, gemma2:9b)\nRun 'ollama list' to see installed models, or 'ollama pull <name>' to install one", model)
		}
	}
	return nil
}
