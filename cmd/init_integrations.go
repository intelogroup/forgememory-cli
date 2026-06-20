package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
)

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	interactive := fs.Bool("interactive", false, "Interactive provider setup wizard")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	fmt.Println("Initializing Forge...")

	// Create database
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	database.Close()
	fmt.Printf("  Database created: %s\n", database.Path)

	// Detect agents
	home, _ := os.UserHomeDir()
	agents := syncIntegrations(home)
	if len(agents) == 0 {
		fmt.Println("  No agents detected. Install Claude Code, Gemini CLI, or Codex first.")
	} else {
		fmt.Printf("  Detected agents: %v\n", agents)
	}

	// Prompt for provider setup
	fmt.Println("\n  Configure your AI provider:")
	fmt.Println("    forge config --provider forgememo   # auto-cheapest, 1000 free runs")
	fmt.Println("    forge config --provider anthropic --api-key YOUR_KEY")
	fmt.Println("  Or run 'forge config' for interactive setup.")
	fmt.Println("  Default provider is forgememo (falls back to ollama if configured).")

	if *interactive {
		fmt.Println("\n  Running interactive setup...")
		runConfig([]string{"--interactive"})
	}

	// Show MCP restart notice for Claude Code
	if contains(agents, "claude") {
		fmt.Println("\n  NOTE: Claude Code requires restart to pick up MCP changes.")
		fmt.Println("  Run 'forge start' to start the daemon, then restart Claude Code.")
	}

	// Warn if multiple forge binaries are found in PATH.
	if installs := findForgeInstalls(); len(installs) > 1 {
		fmt.Println("\nWarning: multiple forge installations found in PATH:")
		for _, fi := range installs {
			fmt.Printf("  %s  (v%s)\n", fi.Path, fi.Version)
		}
		fmt.Println("  Unexpected behavior may occur if different versions are active.")
		fmt.Println("  Run 'forge doctor' for details, or 'forge doctor --repair' to clean up.")
	}

	fmt.Println("\nForge initialized. Run `forge start` to start the daemon.")
}

func runSyncIntegrations(args []string) {
	fmt.Println("Refreshing Forge integrations...")
	home, _ := os.UserHomeDir()
	checkAndRepairIntegrationPaths(home)
	agents := syncIntegrations(home)
	if len(agents) == 0 {
		fmt.Println("  No agents detected.")
		return
	}
	fmt.Printf("  Refreshed agents: %v\n", agents)
}

// checkAndRepairIntegrationPaths detects when hook/MCP binary paths in agent
// settings are stale (pointing to an old forge binary after an upgrade) and
// silently re-runs sync-integrations to fix them.
func checkAndRepairIntegrationPaths(home string) {
	currentPath := agent.ForgePath()
	currentReal := resolveRealPath(currentPath)
	currentHash := fileSHA256(currentPath)

	staleDetected := false

	// Helper to check if a command string is stale
	checkCommandStale := func(cmd string) bool {
		trimmed := strings.TrimSpace(cmd)
		if trimmed == "" {
			return false
		}
		var binaryPart string
		if strings.HasPrefix(trimmed, `"`) {
			binaryPart = strings.SplitN(trimmed[1:], `"`, 2)[0]
		} else {
			binaryPart = strings.SplitN(trimmed, " ", 2)[0]
		}
		if binaryPart == "" || (!strings.Contains(binaryPart, "forge") && !strings.Contains(binaryPart, "forgememo")) {
			return false
		}
		return !isEquivalentBinary(binaryPart, currentPath, currentReal, currentHash)
	}

	// 1. Claude settings.json
	claudePath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(claudePath); err == nil {
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err == nil {
			hooks, _ := settings["hooks"].(map[string]any)
			for _, arr := range hooks {
				items, _ := arr.([]any)
				for _, item := range items {
					m, _ := item.(map[string]any)
					inner, _ := m["hooks"].([]any)
					for _, h := range inner {
						hm, _ := h.(map[string]any)
						if cmd, _ := hm["command"].(string); checkCommandStale(cmd) {
							staleDetected = true
							break
						}
					}
					if staleDetected {
						break
					}
				}
				if staleDetected {
					break
				}
			}
		}
	}

	// 2. Gemini settings.json
	geminiPath := filepath.Join(home, ".gemini", "settings.json")
	if !staleDetected {
		if data, err := os.ReadFile(geminiPath); err == nil {
			var settings map[string]any
			if err := json.Unmarshal(data, &settings); err == nil {
				hooks, _ := settings["hooks"].(map[string]any)
				for _, arr := range hooks {
					items, _ := arr.([]any)
					for _, item := range items {
						m, _ := item.(map[string]any)
						inner, _ := m["hooks"].([]any)
						for _, h := range inner {
							hm, _ := h.(map[string]any)
							if cmd, _ := hm["command"].(string); checkCommandStale(cmd) {
								staleDetected = true
								break
							}
						}
						if staleDetected {
							break
						}
					}
					if staleDetected {
						break
					}
				}
			}
		}
	}

	// 3. Antigravity hooks.json
	antigravityPath := filepath.Join(home, ".gemini", "config", "hooks.json")
	if !staleDetected {
		if data, err := os.ReadFile(antigravityPath); err == nil {
			var hooksConfig map[string]any
			if err := json.Unmarshal(data, &hooksConfig); err == nil {
				if forgeHooks, ok := hooksConfig["forge"].(map[string]any); ok {
					for _, arr := range forgeHooks {
						items, _ := arr.([]any)
						for _, item := range items {
							m, _ := item.(map[string]any)
							inner, _ := m["hooks"].([]any)
							for _, h := range inner {
								hm, _ := h.(map[string]any)
								if cmd, _ := hm["command"].(string); checkCommandStale(cmd) {
									staleDetected = true
									break
								}
							}
							if staleDetected {
								break
							}
						}
						if staleDetected {
							break
						}
					}
				}
			}
		}
	}

	// 4. Codex skill forge.json
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	codexPath := filepath.Join(codexHome, "skills", "forge.json")
	if !staleDetected {
		if data, err := os.ReadFile(codexPath); err == nil {
			var skill map[string]any
			if err := json.Unmarshal(data, &skill); err == nil {
				if hooks, ok := skill["hooks"].(map[string]any); ok {
					for _, cmdVal := range hooks {
						if cmd, ok := cmdVal.(string); ok && checkCommandStale(cmd) {
							staleDetected = true
							break
						}
					}
				}
			}
		}
	}

	// 5. OpenCode plugins/forge.js or opencode.json
	opencodeConfigDir := func() string {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "opencode")
		}
		return filepath.Join(home, ".config", "opencode")
	}()
	opencodePluginPath := filepath.Join(opencodeConfigDir, "plugins", "forge.js")
	if !staleDetected {
		if data, err := os.ReadFile(opencodePluginPath); err == nil {
			content := string(data)
			idx := strings.Index(content, "const FORGE_PATH =")
			if idx >= 0 {
				sub := content[idx:]
				firstQuote := strings.Index(sub, `"`)
				if firstQuote >= 0 {
					sub2 := sub[firstQuote+1:]
					secondQuote := strings.Index(sub2, `"`)
					if secondQuote >= 0 {
						binaryPart := sub2[:secondQuote]
						if !isEquivalentBinary(binaryPart, currentPath, currentReal, currentHash) {
							staleDetected = true
						}
					}
				}
			}
		}
	}
	opencodeJsonPath := filepath.Join(opencodeConfigDir, "opencode.json")
	if !staleDetected {
		if data, err := os.ReadFile(opencodeJsonPath); err == nil {
			var config map[string]any
			if err := json.Unmarshal(data, &config); err == nil {
				if mcp, ok := config["mcp"].(map[string]any); ok {
					if forge, ok := mcp["forge"].(map[string]any); ok {
						if cmdArr, ok := forge["command"].([]any); ok && len(cmdArr) > 0 {
							if cmd, ok := cmdArr[0].(string); ok {
								if !isEquivalentBinary(cmd, currentPath, currentReal, currentHash) {
									staleDetected = true
								}
							}
						}
					}
				}
			}
		}
	}

	if !staleDetected {
		return
	}

	fmt.Println("  Detected stale hook binary in one or more agent settings. Refreshing integrations...")
	syncIntegrations(home)
	fmt.Printf("  Integrations updated to use %s.\n", currentPath)
}

// findStaleForgePathInHooks walks the hooks section of claude settings.json and
// returns the first forge binary path that is genuinely stale, or "" if every
// registered path resolves to the same binary as currentPath (or no forge hooks
// exist).
//
// "Stale" means: the path no longer exists on disk, OR it points to a binary
// whose contents differ from currentPath. String inequality alone is NOT
// enough — npm wrappers, multiple installs, and symlinks all produce
// equivalent binaries at different paths and used to trigger a re-write on
// every `forge start`, churning the agent skill files.
func findStaleForgePathInHooks(settings map[string]any, currentPath string) string {
	currentReal := resolveRealPath(currentPath)
	currentHash := fileSHA256(currentPath)

	hooks, _ := settings["hooks"].(map[string]any)
	for _, arr := range hooks {
		items, _ := arr.([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			inner, _ := m["hooks"].([]any)
			for _, h := range inner {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				// Hook command looks like: "/path/to/forge" hook
				// Extract the binary portion (up to the first space).
				binaryPart := strings.SplitN(strings.Trim(cmd, `"`), `" `, 2)[0]
				binaryPart = strings.TrimPrefix(binaryPart, `"`)
				if binaryPart == "" || !strings.Contains(binaryPart, "forge") {
					continue
				}
				if isEquivalentBinary(binaryPart, currentPath, currentReal, currentHash) {
					continue
				}
				return binaryPart
			}
		}
	}
	return ""
}

// isEquivalentBinary reports whether registeredPath and currentPath should be
// treated as the same forge binary for integration purposes.
func isEquivalentBinary(registeredPath, currentPath, currentReal, currentHash string) bool {
	if normalizePath(registeredPath) == normalizePath(currentPath) {
		return true
	}
	// If the registered path no longer exists, it must be refreshed.
	if _, err := os.Stat(registeredPath); err != nil {
		return false
	}
	if currentReal != "" {
		if r := resolveRealPath(registeredPath); r != "" && normalizePath(r) == normalizePath(currentReal) {
			return true
		}
	}
	// As a last resort, compare file contents — covers the npm-wrapper and
	// multi-install-same-version cases where two paths point to byte-identical
	// binaries on different filesystem roots.
	if currentHash != "" {
		if h := fileSHA256(registeredPath); h != "" && h == currentHash {
			return true
		}
	}
	return false
}

func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(abs)))
}

func resolveRealPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func fileSHA256(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// warnEnvVsConfig prints a warning when shell env vars override config file values
// that the daemon cannot see (the daemon reads only ~/.forge/config at startup).
func warnEnvVsConfig() {
	fileCfg, err := config.Load()
	if err != nil {
		return
	}
	type check struct {
		envKey    string
		fileValue string
		flag      string
	}
	checks := []check{
		{"FORGE_PROVIDER", fileCfg.Provider, "--provider"},
		{"FORGE_API_KEY", fileCfg.APIKey, "--api-key"},
		{"FORGE_MODEL", fileCfg.Model, "--model"},
	}
	for _, c := range checks {
		shellVal := os.Getenv(c.envKey)
		if shellVal != "" && shellVal != c.fileValue {
			fmt.Fprintf(os.Stderr, "  Warning: %s is set in your shell (%q) but not in ~/.forge/config.\n", c.envKey, shellVal)
			fmt.Fprintf(os.Stderr, "           The daemon reads only ~/.forge/config. Run: forge config %s %s\n", c.flag, shellVal)
		}
	}
}

func syncIntegrations(home string) []string {
	agents := agent.DetectAgents(home)
	for _, a := range agents {
		skillPath, err := agent.SetupAgent(a, home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error setting up %s: %v\n", a, err)
		} else if skillPath != "" {
			fmt.Printf("  Configured %s (skill: %s)\n", a, skillPath)
		} else {
			fmt.Printf("  Configured %s\n", a)
		}
	}
	return agents
}
