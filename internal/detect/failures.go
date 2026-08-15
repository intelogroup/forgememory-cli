package detect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/retrieve"
)

var (
	ansiPattern       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	pathPattern       = regexp.MustCompile(`(?:[A-Za-z]:)?(?:[\\/][^:\s"'` + "`" + `]+)+`)
	lineNumPattern    = regexp.MustCompile(`:\d+(?::\d+)?`)
	longNumberPattern = regexp.MustCompile(`\b\d{2,}\b`)

	rustCodePattern     = regexp.MustCompile(`error\[(e\d{4})\]`)
	npmErrPattern       = regexp.MustCompile(`npm err!?`)
	pnpmErrPattern      = regexp.MustCompile(` err_pnpm_[a-z0-9_]+`)
	goUndefinedPattern  = regexp.MustCompile(`undefined: [a-z0-9_./*]+`)
	pythonTracePattern  = regexp.MustCompile(`traceback \(most recent call last\):`)
	genericFailPattern  = regexp.MustCompile(`(?i)\b(error|failed|failure|panic|exception|fatal|undefined:|could not compile|cannot find|not found|timed out)\b`)
	// sourceCodeDeclPattern matches source code declaration lines that carry
	// identifiers or parameters named "error"/"errors" but are not runtime failures.
	sourceCodeDeclPattern = regexp.MustCompile(`(?i)^\s*(def |async def |func |fn |pub fn |class |public |private |protected )`)
	successTextPatterns   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bbuild succeeded\b`),
		regexp.MustCompile(`(?i)\bcompiled successfully\b`),
		regexp.MustCompile(`(?i)\bfinished\b.*\b(target|release|dev)\b`),
		regexp.MustCompile(`(?i)\bsuccessfully\b`),
	}
)

type failureObservation struct {
	Fingerprint       string
	ErrorKind         string
	NormalizedMessage string
	CommandFamily     string
	Title             string
	Narrative         string
	Score             float64
}

type successObservation struct {
	CommandFamily string
}

// ProcessEvent analyzes a raw event for repeating failures and raises alerts.
func ProcessEvent(database *db.DB, event *db.Event) error {
	if event == nil || database == nil {
		return nil
	}
	if event.EventType != "PostToolUse" {
		return nil
	}
	if strings.TrimSpace(event.Payload) == "" {
		return nil
	}

	obs := observeFailure(event)
	if obs != nil {
		sig, err := database.UpsertFailureSignature(&db.FailureSignature{
			ProjectID:         event.ProjectID,
			SessionID:         event.SessionID,
			SourceTool:        event.SourceTool,
			ToolName:          event.ToolName,
			CommandFamily:     obs.CommandFamily,
			Fingerprint:       obs.Fingerprint,
			ErrorKind:         obs.ErrorKind,
			NormalizedMessage: obs.NormalizedMessage,
			LastSeenTS:        event.TS,
		})
		if err != nil {
			return err
		}

		if sig.RepeatCount < 2 {
			return nil
		}

		severity := "medium"
		score := obs.Score + float64(sig.RepeatCount-2)*0.15
		if sig.RepeatCount >= 3 {
			severity = "high"
			score += 0.15
		}

		if err := database.UpsertActiveAlert(&db.Alert{
			TS:          event.TS,
			ProjectID:   event.ProjectID,
			SessionID:   event.SessionID,
			AlertType:   "repeated_failure",
			Severity:    severity,
			Title:       obs.Title,
			Narrative:   fmt.Sprintf("%s This has failed %d times in this session without a detected resolution.", obs.Narrative, sig.RepeatCount),
			Fingerprint: obs.Fingerprint,
			Score:       score,
			SourceRef:   sig.ID,
			ExpiresAt:   time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		}); err != nil {
			return err
		}

		return retrieve.EnqueueFailureRetrieval(database, event.ProjectID, obs.CommandFamily, obs.ErrorKind, obs.NormalizedMessage, event.Payload)
	}

	success := observeSuccess(event)
	if success == nil {
		return nil
	}

	resolvedIDs, err := database.ResolveFailureSignatures(event.ProjectID, event.SessionID, event.ToolName, success.CommandFamily, event.TS)
	if err != nil {
		return err
	}
	return database.AcknowledgeAlerts(event.ProjectID, event.SessionID, "repeated_failure", event.TS, resolvedIDs)
}

func observeFailure(event *db.Event) *failureObservation {
	exitCode, hasExitCode := extractExitCode(event.Payload)
	if hasExitCode && exitCode == 0 {
		// The tool itself reported success. Trust that over text matching —
		// output can legitimately contain words like "error"/"failed" (log
		// lines, retried-then-succeeded steps) without the command failing.
		return nil
	}

	texts := extractStrings(event.Payload)
	if len(texts) == 0 {
		texts = []string{event.Payload}
	}

	command := extractCommandFamily(event.Payload)
	if command == "" {
		command = pickCommandFamily(texts)
	}

	lines := candidateLines(texts)
	if len(lines) == 0 {
		return nil
	}

	bestKind := ""
	bestLine := ""
	for _, line := range lines {
		kind := classifyFailure(line)
		if kind == "" {
			continue
		}
		bestKind = kind
		bestLine = line
		break
	}
	if bestKind == "" {
		if !hasExitCode {
			return nil
		}
		// Non-zero exit code is authoritative failure evidence on its own,
		// even when the output doesn't match a known error pattern (e.g. a
		// custom script that fails silently). Fall back to the first
		// candidate line so the failure still gets fingerprinted instead of
		// being silently dropped.
		bestKind = "tool_error"
		bestLine = lines[0]
	}

	normalized := normalizeFailureLine(bestLine)
	if normalized == "" {
		return nil
	}

	fpSource := strings.Join([]string{
		event.SourceTool,
		event.ToolName,
		command,
		bestKind,
		normalized,
	}, "|")
	sum := sha256.Sum256([]byte(fpSource))
	fingerprint := fmt.Sprintf("%x", sum[:8])

	title := buildAlertTitle(command, bestKind)
	narrative := fmt.Sprintf("Forge keeps seeing the same %s failure signature", bestKind)
	if command != "" {
		narrative = fmt.Sprintf("%s while running %s", narrative, command)
	}
	narrative += fmt.Sprintf(": %s.", normalized)

	score := 1.15
	if strings.HasPrefix(bestKind, "rust") || strings.HasPrefix(bestKind, "npm") || strings.HasPrefix(bestKind, "pnpm") {
		score = 1.35
	}

	return &failureObservation{
		Fingerprint:       fingerprint,
		ErrorKind:         bestKind,
		NormalizedMessage: normalized,
		CommandFamily:     command,
		Title:             title,
		Narrative:         narrative,
		Score:             score,
	}
}

func observeSuccess(event *db.Event) *successObservation {
	texts := extractStrings(event.Payload)
	if len(texts) == 0 {
		texts = []string{event.Payload}
	}

	command := extractCommandFamily(event.Payload)
	if command == "" {
		command = pickCommandFamily(texts)
	}
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if isLikelySuccess(line) {
				return &successObservation{CommandFamily: command}
			}
		}
	}
	return nil
}

// extractStrings returns text strings to scan for failure/success signals.
// When the payload is a hook JSON object with a tool_response key, only that
// subtree is scanned — this prevents false positives from file content inside
// tool_input fields (old_string, new_string, content, etc.).
func extractStrings(payload string) []string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}

	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return []string{payload}
	}

	if obj, ok := parsed.(map[string]any); ok {
		if toolResp, found := obj["tool_response"]; found {
			var out []string
			walkForStrings(toolResp, &out)
			return out
		}
	}

	var out []string
	walkForStrings(parsed, &out)
	return out
}

func walkForStrings(node any, out *[]string) {
	switch value := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkForStrings(value[key], out)
		}
	case []any:
		for _, child := range value {
			walkForStrings(child, out)
		}
	case string:
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			*out = append(*out, trimmed)
		}
	}
}

// extractCommandFamily reads the command from tool_input.command in the raw
// payload and returns its family (e.g. "cargo build", "npm install"). Reading
// directly from tool_input is more reliable than scanning output strings.
func extractCommandFamily(payload string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return ""
	}
	input, ok := obj["tool_input"].(map[string]any)
	if !ok {
		return ""
	}
	cmd, _ := input["command"].(string)
	return commandFamily(strings.TrimSpace(cmd))
}

// extractExitCode reads tool_response.exit_code from the raw payload. It
// returns found=false when the payload has no exit_code field (e.g. non-shell
// tools like Read/Edit/Write, or older hook payloads) so callers can fall
// back to text-based heuristics.
func extractExitCode(payload string) (code int, found bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return 0, false
	}
	resp, ok := obj["tool_response"]
	if !ok {
		return 0, false
	}
	return nestedExitCode(resp)
}

func nestedExitCode(value any) (int, bool) {
	switch node := value.(type) {
	case map[string]any:
		if raw, ok := node["exit_code"]; ok {
			switch code := raw.(type) {
			case float64:
				return int(code), true
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(code)); err == nil {
					return n, true
				}
			}
		}
		for _, child := range node {
			if code, found := nestedExitCode(child); found {
				return code, true
			}
		}
	case []any:
		for _, child := range node {
			if code, found := nestedExitCode(child); found {
				return code, true
			}
		}
	}
	return 0, false
}

func candidateLines(texts []string) []string {
	var lines []string
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if isLikelySuccess(line) {
				continue
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func isLikelySuccess(line string) bool {
	for _, pattern := range successTextPatterns {
		if pattern.MatchString(line) && !genericFailPattern.MatchString(line) {
			return true
		}
	}
	return false
}

func classifyFailure(line string) string {
	lowered := strings.ToLower(line)
	switch {
	case rustCodePattern.MatchString(lowered):
		return "rustc"
	case strings.Contains(lowered, "could not compile"):
		return "rust_compile"
	case npmErrPattern.MatchString(lowered):
		return "npm"
	case pnpmErrPattern.MatchString(lowered):
		return "pnpm"
	case pythonTracePattern.MatchString(lowered):
		return "python_traceback"
	case goUndefinedPattern.MatchString(lowered):
		return "go_build"
	case genericFailPattern.MatchString(lowered):
		if sourceCodeDeclPattern.MatchString(line) {
			return ""
		}
		return "tool_error"
	default:
		return ""
	}
}

func normalizeFailureLine(line string) string {
	line = ansiPattern.ReplaceAllString(line, "")
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return ""
	}
	line = pathPattern.ReplaceAllStringFunc(line, func(value string) string {
		base := filepath.Base(strings.ReplaceAll(value, `\`, `/`))
		if base == "." || base == "" {
			return "<path>"
		}
		return "<path:" + base + ">"
	})
	line = lineNumPattern.ReplaceAllString(line, "")
	line = longNumberPattern.ReplaceAllString(line, "<n>")
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > 220 {
		line = line[:220]
	}
	return line
}

func buildAlertTitle(command, kind string) string {
	if strings.HasPrefix(kind, "rust") {
		if command != "" {
			return "Repeated Rust compiler failure"
		}
		return "Repeated Rust build failure"
	}
	switch kind {
	case "npm":
		return "Repeated npm failure"
	case "pnpm":
		return "Repeated pnpm failure"
	case "go_build":
		return "Repeated Go build failure"
	default:
		if command != "" {
			return "Repeated " + command + " failure"
		}
		return "Repeated tool failure"
	}
}

func commandFamily(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	head := fields[0]
	if looksLikePath(head) || strings.Contains(head, "[") || strings.Contains(head, ":") {
		return ""
	}

	switch head {
	case "cargo", "go", "npm", "pnpm", "yarn", "bun", "rustc", "python", "pytest", "node", "vercel":
		if len(fields) == 1 {
			return head
		}
		return head + " " + fields[1]
	default:
		return ""
	}
}

// pickCommandFamily selects the best commandFamily from a set of strings extracted
// from a JSON payload. It prefers strings that look like shell commands (contain
// spaces, start with a known tool name, no path separators). Falls back to the
// longest non-path string, then the first string.
func pickCommandFamily(texts []string) string {
	knownTools := map[string]bool{
		"cargo": true, "go": true, "npm": true, "pnpm": true,
		"yarn": true, "bun": true, "rustc": true, "python": true,
		"pytest": true, "node": true, "vercel": true,
	}

	// Pass 1: strings that contain spaces, start with a known tool.
	// Path separators in arguments (e.g. "go test ./...") are allowed;
	// only reject strings whose command head looks like a path.
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if !strings.Contains(text, " ") {
			continue
		}
		head := strings.Fields(text)[0]
		if strings.ContainsAny(head, "/\\") {
			continue
		}
		if knownTools[head] {
			if fam := commandFamily(text); fam != "" {
				return fam
			}
		}
	}

	// Pass 2: any string whose commandFamily resolves; reject only path-like heads.
	for _, text := range texts {
		text = strings.TrimSpace(text)
		head := strings.Fields(text)[0]
		if strings.ContainsAny(head, "/\\") {
			continue
		}
		if fam := commandFamily(text); fam != "" {
			return fam
		}
	}

	// Pass 3: longest string whose head is not a path.
	longest := ""
	for _, text := range texts {
		text = strings.TrimSpace(text)
		head := strings.Fields(text)[0]
		if strings.ContainsAny(head, "/\\") {
			continue
		}
		if len(text) > len(longest) {
			longest = text
		}
	}
	if fam := commandFamily(longest); fam != "" {
		return fam
	}

	// Pass 4: first string, regardless.
	if len(texts) > 0 {
		return commandFamily(texts[0])
	}
	return ""
}

func looksLikePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, `\`) {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	return false
}
