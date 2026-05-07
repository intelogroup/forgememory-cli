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
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return // no claude settings, nothing to repair
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}

	// Scan all hook command strings for a forge binary path that differs from
	// the current binary.
	stalePath := findStaleForgePathInHooks(settings, currentPath)
	if stalePath == "" {
		return
	}

	fmt.Printf("  Detected stale hook binary (%s). Refreshing integrations...\n", stalePath)
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
	if registeredPath == currentPath {
		return true
	}
	// If the registered path no longer exists, it must be refreshed.
	if _, err := os.Stat(registeredPath); err != nil {
		return false
	}
	if currentReal != "" {
		if r := resolveRealPath(registeredPath); r != "" && r == currentReal {
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
