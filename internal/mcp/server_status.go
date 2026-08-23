package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) getDistillationHealth(args map[string]any) ToolResult {
	health, err := s.db.GetDistillationHealth()
	if err != nil {
		return toolError("Failed to get distillation health: " + err.Error())
	}
	_, undistilled, _ := s.db.EventCount()

	payload := map[string]any{
		"last_run_at":           health.LastRunAt,
		"last_success_at":       health.LastSuccessAt,
		"status":                health.LastStatus,
		"error_message":         health.LastErrorMessage,
		"undistilled_count":     undistilled,
		"last_error_at":         health.LastErrorAt,
		"consecutive_failures":  health.ConsecutiveFailures,
		"next_scheduled_at":     health.NextScheduledAt,
		"last_attempted_events": health.LastAttemptedEvents,
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: string(b)}},
	}
}

func (s *Server) getAlerts(args map[string]any) ToolResult {
	health, err := s.db.GetDistillationHealth()
	if err != nil {
		return toolError("Failed to get alerts: " + err.Error())
	}
	_, undistilled, _ := s.db.EventCount()

	type alert struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	}
	var alerts []alert
	if health.ConsecutiveFailures >= 1 {
		severity := "warning"
		if health.ConsecutiveFailures >= 3 {
			severity = "error"
		}
		alerts = append(alerts, alert{
			Type:     "distillation_failed",
			Severity: severity,
			Message:  fmt.Sprintf("%d consecutive failures", health.ConsecutiveFailures),
		})
	}
	if undistilled >= 25 {
		alerts = append(alerts, alert{
			Type:     "undistilled_backlog",
			Severity: "warning",
			Message:  fmt.Sprintf("%d undistilled events", undistilled),
		})
	}
	b, _ := json.MarshalIndent(alerts, "", "  ")
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: string(b)}},
	}
}

// --- get_forge_status helpers ---

func forgeHomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return home
}

func mcpReadAddr() string {
	data, err := os.ReadFile(filepath.Join(forgeHomeDir(), ".forge", "forge.addr"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func mcpReadPID() int {
	data, err := os.ReadFile(filepath.Join(forgeHomeDir(), ".forge", "forge.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func mcpIsDaemonAlive(addr string) bool {
	if addr == "" {
		return false
	}
	network := "unix"
	if strings.Contains(addr, ":") {
		network = "tcp"
	}
	conn, err := net.DialTimeout(network, addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// mcpHooksRegistered checks ~/.claude/settings.json for forge hook entries.
// Returns (found bool, hookCount int).
func mcpHooksRegistered() (bool, int) {
	settingsPath := filepath.Join(forgeHomeDir(), ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false, 0
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, 0
	}
	hooks, _ := settings["hooks"].(map[string]any)
	count := 0
	for _, arr := range hooks {
		items, _ := arr.([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			inner, _ := m["hooks"].([]any)
			for _, h := range inner {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if strings.Contains(cmd, "forge") {
					count++
				}
			}
		}
	}
	return count > 0, count
}

// mcpLastEventTS returns the timestamp of the most recent event in the DB, or "".
func (s *Server) mcpLastEventTS() string {
	var ts string
	_ = s.db.Conn().QueryRow(`SELECT ts FROM events ORDER BY ts DESC LIMIT 1`).Scan(&ts)
	return ts
}

func (s *Server) getForgeStatus(_ map[string]any) ToolResult {
	var sb strings.Builder
	sb.WriteString("## Forge System Status\n\n")

	anyProblem := false

	// 1. Daemon
	addr := mcpReadAddr()
	pid := mcpReadPID()
	daemonAlive := mcpIsDaemonAlive(addr)
	sb.WriteString("### Daemon\n")
	if daemonAlive {
		sb.WriteString(fmt.Sprintf("- Status: OK (pid %d, addr %s)\n", pid, addr))
	} else if addr != "" {
		sb.WriteString(fmt.Sprintf("- Status: STALE — addr file exists (%s) but daemon not responding\n", addr))
		sb.WriteString("- Fix: `forge stop && forge start`\n")
		anyProblem = true
	} else {
		sb.WriteString("- Status: NOT RUNNING — no daemon detected\n")
		sb.WriteString("- Fix: `forge start`\n")
		anyProblem = true
	}
	sb.WriteString("\n")

	// 2. Hooks
	sb.WriteString("### Hooks (event ingestion)\n")
	hooksFound, hookCount := mcpHooksRegistered()
	if hooksFound {
		sb.WriteString(fmt.Sprintf("- Registered: YES (%d hook entries in ~/.claude/settings.json)\n", hookCount))
	} else {
		sb.WriteString("- Registered: NO — forge hooks not found in ~/.claude/settings.json\n")
		sb.WriteString("- Fix: `forge init` then restart Claude Code\n")
		anyProblem = true
	}

	// 3. Event ingestion — last event age
	lastEventTS := s.mcpLastEventTS()
	if lastEventTS != "" {
		parsed, err := time.Parse(time.RFC3339, lastEventTS)
		if err == nil {
			age := time.Since(parsed)
			ageStr := formatAge(age)
			if age > 2*time.Hour && daemonAlive && hooksFound {
				sb.WriteString(fmt.Sprintf("- Last event: %s ago — WARNING: no events received recently despite daemon running\n", ageStr))
				sb.WriteString("- Fix: restart Claude Code to reload hooks; run `forge doctor` for diagnostics\n")
				anyProblem = true
			} else {
				sb.WriteString(fmt.Sprintf("- Last event: %s ago\n", ageStr))
			}
		}
	} else {
		sb.WriteString("- Last event: none recorded yet\n")
	}
	sb.WriteString("\n")

	// 4. Distillation
	sb.WriteString("### Distillation\n")
	health, err := s.db.GetDistillationHealth()
	_, undistilled, _ := s.db.EventCount()
	if err != nil {
		sb.WriteString(fmt.Sprintf("- Status: ERROR reading health: %v\n", err))
		anyProblem = true
	} else {
		if health.ConsecutiveFailures == 0 && health.LastSuccessAt != "" {
			sb.WriteString(fmt.Sprintf("- Status: OK (last success: %s)\n", formatRelativeTS(health.LastSuccessAt)))
		} else if health.LastSuccessAt == "" && health.ConsecutiveFailures == 0 {
			sb.WriteString("- Status: PENDING — no distillation run yet\n")
		} else if health.ConsecutiveFailures > 0 {
			severity := "WARNING"
			if health.ConsecutiveFailures >= 3 {
				severity = "ERROR"
			}
			sb.WriteString(fmt.Sprintf("- Status: %s — %d consecutive failure(s)\n", severity, health.ConsecutiveFailures))
			if health.LastErrorMessage != "" {
				sb.WriteString(fmt.Sprintf("- Error: %s\n", health.LastErrorMessage))
			}
			sb.WriteString("- Fix: `forge config` to verify provider/api-key; `forge distill` to retry manually\n")
			anyProblem = true
		}
		sb.WriteString(fmt.Sprintf("- Undistilled events queued: %d (threshold: 300)\n", undistilled))
		if undistilled > 0 && undistilled < 300 {
			sb.WriteString(fmt.Sprintf("- Note: distillation triggers at 300 events (%d more needed)\n", 300-undistilled))
		}
	}
	sb.WriteString("\n")

	// 5. Summary
	if !anyProblem {
		sb.WriteString("### Summary: ALL SYSTEMS OK\n")
	} else {
		sb.WriteString("### Summary: ACTION REQUIRED — see Fix lines above\n")
		sb.WriteString("- Run `forge doctor` for full diagnostics\n")
		sb.WriteString("- Run `forge status` for DB stats\n")
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: sb.String()}},
	}
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func formatRelativeTS(ts string) string {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return formatAge(time.Since(parsed)) + " ago"
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Server) reconfigureProvider(args map[string]any) ToolResult {
	reason, _ := args["reason"].(string)
	if reason == "" {
		reason = "unspecified failure"
	}

	configPath := filepath.Join(forgeHomeDir(), ".forge", "config")
	attemptsPath := filepath.Join(forgeHomeDir(), ".forge", "reconfig_attempts")

	// Read attempts count
	count := 0
	attemptsData, err := os.ReadFile(attemptsPath)
	if err == nil {
		if val, parseErr := strconv.Atoi(strings.TrimSpace(string(attemptsData))); parseErr == nil {
			count = val
		}
	}

	// Check if config has been modified since attempts were logged to reset count
	if configInfo, statErr := os.Stat(configPath); statErr == nil {
		if attemptsInfo, attemptsStatErr := os.Stat(attemptsPath); attemptsStatErr == nil {
			if configInfo.ModTime().After(attemptsInfo.ModTime()) {
				count = 0
			}
		}
	}

	if count >= 3 {
		return ToolResult{
			IsError: true,
			Content: []ToolContent{{
				Type: "text",
				Text: "Error: Reconfiguration attempt limit exceeded (3). Please run 'forge config --interactive' manually in your primary terminal shell to repair the credentials, or run 'forge doctor' to diagnose the system state.",
			}},
		}
	}

	count++
	_ = os.WriteFile(attemptsPath, []byte(strconv.Itoa(count)), 0o600)

	msg := fmt.Sprintf("### [ForgeMemory Alert]\nThe inference provider encountered a failure:\n> %s\n\nPlease open your primary terminal shell and run the following command to reconfigure your provider:\n```bash\nforge config --interactive\n```\n(Attempt %d of 3. After 3 attempts, this automated action will be blocked until a configuration change is saved).", reason, count)

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: msg}},
	}
}
