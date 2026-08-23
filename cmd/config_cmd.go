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
	providerFlag := fs.String("provider", "", "Provider: anthropic/openai/groq/nvidia/ollama/antigravity/openrouter/gemini")
	apiKeyFlag := fs.String("api-key", "", "API key for provider")
	modelFlag := fs.String("model", "", "Model name (optional, defaults vary by provider)")
	baseURLFlag := fs.String("base-url", "", "Base URL for API (optional)")
	context7APIKeyFlag := fs.String("context7-api-key", "", "Context7 API key for official docs")
	timeoutFlag := fs.String("timeout", "", "Inference timeout (e.g. 30s)")
	retriesFlag := fs.Int("retries", -1, "Retry attempts for transient failures")
	intervalFlag := fs.String("interval", "", "Distillation interval (e.g. 10m)")
	ollamaTimeoutFlag := fs.String("ollama-timeout", "", "Ollama request timeout (e.g. 120s); overrides --timeout for Ollama")
	ollamaStartupWaitFlag := fs.String("ollama-startup-wait", "", "Grace period before first Ollama distill (e.g. 15s)")
	distillBatchSizeFlag := fs.Int("distill-batch-size", 0, "Number of events to distill per batch (e.g. 300)")
	jsonFlag := fs.Bool("json", false, "Machine-readable JSON output")
	validateFlag := fs.Bool("validate", false, "Validate provider credentials/model access (now default; kept for compatibility)")
	noValidateFlag := fs.Bool("no-validate", false, "Skip credential validation after saving (useful for offline scripts or custom OpenAI-compatible endpoints)")
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
			fmt.Println("No config set. Run 'forge config --provider openai --api-key sk-...' or configure another provider.")
			return
		}
		if *jsonFlag {
			out := map[string]any{
				"schema_version":     "1",
				"provider":           cfg.Provider,
				"api_key_set":        cfg.APIKey != "",
				"api_key_masked":     maskKey(cfg.APIKey),
				"model":              cfg.Model,
				"base_url":           cfg.BaseURL,
				"timeout":            cfg.Timeout,
				"retries":            cfg.Retries,
				"interval":           cfg.DistillInterval,
				"distill_batch_size": cfg.DistillBatchSize,
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
		if cfg.DistillBatchSize > 0 {
			fmt.Printf("Distill batch size:   %d\n", cfg.DistillBatchSize)
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
		fmt.Println("  --provider       Provider (all accepted values):")
		fmt.Println("                     anthropic, openai, groq, nvidia, ollama, antigravity, gemini,")
		fmt.Println("                     openrouter, codex")
		fmt.Println("  --api-key        API key for the provider")
		fmt.Println("  --model          Model name (optional)")
		fmt.Println("  --base-url       Base URL for API (optional; required for openrouter/custom endpoints)")
		fmt.Println("  --context7-api-key Context7 API key for official docs")
		fmt.Println("  --timeout              Inference timeout (e.g. 30s)")
		fmt.Println("  --retries              Retry attempts for transient failures")
		fmt.Println("  --interval             Distillation interval (e.g. 10m)")
		fmt.Println("  --ollama-timeout       Ollama request timeout (e.g. 120s); overrides --timeout for Ollama")
		fmt.Println("  --ollama-startup-wait  Grace period before first Ollama distill (e.g. 15s)")
		fmt.Println("  --distill-batch-size   Number of events to distill per batch (e.g. 300)")
		fmt.Println("  --validate       Force credential validation (default when credentials change)")
		fmt.Println("  --no-validate    Skip credential validation (for offline scripts or custom endpoints)")
		fmt.Println("  --json           JSON output (with --show)")
		fmt.Println("  --interactive    Interactive setup")
		fmt.Println("")
		fmt.Println("Defaults by provider:")
		fmt.Println("  anthropic:   claude-haiku-4-5-20251001")
		fmt.Println("  openai:      gpt-4o")
		fmt.Println("  groq:        llama-3.3-70b-versatile (uses GROQ_API_KEY env or --api-key)")
		fmt.Println("  nvidia:      meta/llama-3.3-70b-instruct (uses NVIDIA_API_KEY env or --api-key)")
		fmt.Println("  ollama:      llama3:latest")
		fmt.Println("  openrouter:  google/gemini-2.5-flash (use --base-url https://openrouter.ai/api/v1)")
		fmt.Println("  antigravity: flash")
		fmt.Println("")
		fmt.Println("Note: config changes take effect after restarting the daemon (forge stop && forge start).")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  forge config --show")
		fmt.Println("  forge config --provider openai --api-key sk-...")
		fmt.Println("  forge config --provider nvidia --api-key nvapi-...")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-...")
		fmt.Println("  forge config --provider groq --api-key gsk-...")
		fmt.Println("  forge config --provider ollama --model llama3:latest")
		fmt.Println("  forge config --provider openrouter --api-key sk-or-v1-... --model google/gemini-2.5-flash")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-... --validate")
		fmt.Println("  forge config --provider openai --context7-api-key ctx7sk-...")
		os.Exit(0)
	}

	validProviders := map[string]bool{"anthropic": true, "openai": true, "ollama": true, "groq": true, "nvidia": true, "codex": true, "antigravity": true, "gemini": true, "openrouter": true}
	if !validProviders[*providerFlag] {
		fmt.Fprintf(os.Stderr, "Error: provider must be one of: anthropic, openai, groq, nvidia, ollama, codex, antigravity, gemini, openrouter\n")
		os.Exit(1)
	}
	if *providerFlag == "gemini" {
		*providerFlag = "antigravity"
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
	if *distillBatchSizeFlag < 0 || (*distillBatchSizeFlag > 0 && *distillBatchSizeFlag < 300) {
		fmt.Fprintln(os.Stderr, "Error: --distill-batch-size must be 0 (default) or >= 300")
		os.Exit(1)
	}
	if *retriesFlag < -1 {
		fmt.Fprintln(os.Stderr, "Error: --retries must be >= 0")
		os.Exit(1)
	}

	existing, _ := config.Load()
	cfg := existing
	providerChanged := false
	if *providerFlag != "" && *providerFlag != existing.Provider {
		cfg.Provider = *providerFlag
		providerChanged = true
		// Reset provider-specific fields if the provider changes
		cfg.APIKey = ""
		cfg.Model = ""
		cfg.BaseURL = ""
	} else if *providerFlag != "" {
		cfg.Provider = *providerFlag
	}

	if *apiKeyFlag != "" {
		cfg.APIKey = *apiKeyFlag
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	} else if cfg.Model == "" || providerChanged {
		cfg.Model = defaultModelForProvider(cfg.Provider)
	}
	if *baseURLFlag != "" {
		cfg.BaseURL = *baseURLFlag
	} else if cfg.BaseURL == "" || providerChanged {
		cfg.BaseURL = defaultBaseURLForProvider(cfg.Provider)
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
	if *distillBatchSizeFlag > 0 {
		cfg.DistillBatchSize = *distillBatchSizeFlag
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}

	// Catch obvious provider/model mismatches locally before touching the network.
	if err := config.CheckProviderModelCompat(cfg.Provider, cfg.Model, cfg.APIKey, cfg.BaseURL); err != nil {
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
	if cfg.DistillBatchSize > 0 {
		fmt.Printf("Distill Batch Size: %d\n", cfg.DistillBatchSize)
	}
	fmt.Println("\nNote: the daemon caches config at startup — restart it to apply:")
	fmt.Println("  forge stop && forge start")
}

func runConfigInteractive(providerFlag, apiKeyFlag, modelFlag, timeoutFlag *string, retriesFlag *int, intervalFlag *string) {
	reader := bufio.NewReader(os.Stdin)
	isTTY := false
	if fi, err := os.Stdin.Stat(); err == nil {
		isTTY = (fi.Mode() & os.ModeCharDevice) != 0
	}
	if !isTTY {
		return
	}

	if *providerFlag == "" {
		*providerFlag = promptChoice(reader, "Select provider", []string{"anthropic", "openai", "groq", "nvidia", "ollama"}, "ollama")
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
	case "antigravity":
		fmt.Println("? Select model:")
		fmt.Println("  - flash (recommended: fast and balanced)")
		fmt.Println("  - flash_lite (lightweight and fastest)")
		fmt.Println("  - pro (most capable, slowest)")
		return promptInput(reader, "Model", "flash")
	default:
		return defaultModelForProvider(provider)
	}
}

func defaultModelForProvider(provider string) string {
	switch provider {
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
	case "antigravity":
		return "flash"
	case "openrouter":
		return "google/gemini-2.5-flash"
	default:
		return "claude-haiku-4-5-20251001"
	}
}

func distillConfigFromUserConfig(cfg config.Config) distill.Config {
	d := distill.LoadConfig()
	d.Provider = distill.Provider(cfg.Provider)
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

func defaultBaseURLForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com"
	case "groq":
		return "https://api.groq.com/openai"
	case "nvidia":
		return "https://integrate.api.nvidia.com/v1"
	case "ollama":
		return "http://localhost:11434"
	case "antigravity":
		return ""
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	default:
		return ""
	}
}
