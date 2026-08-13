// Package observations derives deterministic, auditable coaching observations
// from existing ForgeMemo events and session records.
package observations

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/forge/forge/internal/db"
)

const (
	verificationSkill = "verification.pre_ship"
	delayedTestWindow = 30 * time.Minute

	targetedVerificationConfidence = 0.75
	broadVerificationConfidence    = 0.65
	delayedConfidenceReduction     = 0.15
	counterConfidenceReduction     = 0.15
	missingVerificationConfidence  = 0.70
	unresolvedFailureConfidence    = 0.90
	regressionConfidence           = 0.95
)

// Input is the bounded session evidence the detector needs. Live is retained
// for the persistence layer, which distinguishes live observations from
// historical backfill without changing detection.
type Input struct {
	ProjectID string
	SessionID string
	Events    []db.Event
	Commits   []db.SessionCommit
	Failures  []db.FailureSignature
	Live      bool
}

// SourceReference identifies, but never copies, raw source material.
type SourceReference struct {
	SourceType string
	SourceID   string
	Excerpt    string
}

// ObservationDraft is an auditable observation ready for evidence storage.
type ObservationDraft struct {
	Kind              string
	SkillKey          string
	Confidence        float64
	Severity          string
	Summary           string
	SupportingSources []SourceReference
	CounterSources    []SourceReference
}

type change struct {
	at     time.Time
	path   string
	source SourceReference
}

type testScope string

const (
	targetedScope testScope = "targeted"
	broadScope    testScope = "broad"
)

type testRun struct {
	at      time.Time
	command string
	scope   testScope
	target  string
	success bool
	source  SourceReference
}

// DetectVerification emits only deterministic, sufficiently strong claims.
// In particular, it never turns a lone session boundary into a gap.
func DetectVerification(input Input) []ObservationDraft {
	changes := collectChanges(input)
	if len(changes) == 0 {
		return nil
	}
	tests := collectTestRuns(input)
	failures := collectFailures(input)

	var drafts []ObservationDraft
	for _, changed := range changes {
		matchingSuccesses, counterTests := matchTests(changed, tests)
		matchingFailures := failuresAfter(changed, failures)

		if len(matchingSuccesses) > 0 {
			best := matchingSuccesses[0]
			confidence := verificationConfidence(best, changed)
			counterSources := testSources(counterTests)
			for _, failure := range matchingFailures {
				if failure.at.After(best.at) && sameTestFamily(best.command, failure.command) {
					counterSources = append(counterSources, failure.source)
				}
			}
			if len(counterSources) > 0 {
				confidence -= counterConfidenceReduction
				if confidence < 0 {
					confidence = 0
				}
			}
			drafts = append(drafts, ObservationDraft{
				Kind:              "verification_detected",
				SkillKey:          verificationSkill,
				Confidence:        confidence,
				Severity:          "info",
				Summary:           verificationSummary(best),
				SupportingSources: []SourceReference{changed.source, best.source},
				CounterSources:    counterSources,
			})
		}

		if len(matchingFailures) > 0 {
			for _, failure := range matchingFailures {
				priorSuccesses := successesBefore(matchingSuccesses, failure.at)
				if len(priorSuccesses) > 0 {
					drafts = append(drafts, ObservationDraft{
						Kind:              "repeated_regression",
						SkillKey:          verificationSkill,
						Confidence:        regressionConfidence,
						Severity:          "error",
						Summary:           "A previously passing test later failed repeatedly after a code change.",
						SupportingSources: []SourceReference{changed.source, failure.source},
						CounterSources:    testSources(priorSuccesses),
					})
				} else {
					drafts = append(drafts, ObservationDraft{
						Kind:              "unresolved_failure_after_change",
						SkillKey:          verificationSkill,
						Confidence:        unresolvedFailureConfidence,
						Severity:          "error",
						Summary:           "A test failure repeated after a code change without a detected resolution.",
						SupportingSources: []SourceReference{changed.source, failure.source},
					})
				}
			}
			continue
		}

		if len(matchingSuccesses) == 0 {
			drafts = append(drafts, ObservationDraft{
				Kind:              "code_change_without_relevant_test",
				SkillKey:          verificationSkill,
				Confidence:        missingVerificationConfidence,
				Severity:          "warning",
				Summary:           "A code change had no detected relevant passing test.",
				SupportingSources: []SourceReference{changed.source},
				CounterSources:    testSources(counterTests),
			})
		}
	}
	return drafts
}

func collectChanges(input Input) []change {
	var commits []change
	for _, commit := range input.Commits {
		if !matchesInput(commit.ProjectID, commit.SessionID, input) || commit.Files <= 0 || commit.Insertions+commit.Deletions <= 0 {
			continue
		}
		id := commit.SHA
		if id == "" {
			id = commit.ID
		}
		commits = append(commits, change{
			at:     parseTime(commit.CommitTS),
			source: SourceReference{SourceType: "commit", SourceID: id, Excerpt: bounded(commit.Subject)},
		})
	}
	if len(commits) > 0 {
		sortChanges(commits)
		return commits
	}

	var changes []change
	for _, event := range input.Events {
		if !matchesInput(event.ProjectID, event.SessionID, input) || !isWriteEvent(event) {
			continue
		}
		path := filePath(event.Payload)
		if !isMeaningfulCodePath(path) || isFormattingEvent(event) || isWhitespaceOnlyEdit(event.Payload) {
			continue
		}
		changes = append(changes, change{
			at:     parseTime(event.TS),
			path:   cleanPath(path),
			source: SourceReference{SourceType: "event", SourceID: event.ID, Excerpt: bounded(path)},
		})
	}
	sortChanges(changes)
	return changes
}

func collectTestRuns(input Input) []testRun {
	var runs []testRun
	for _, event := range input.Events {
		if !matchesInput(event.ProjectID, event.SessionID, input) || !isToolEvent(event) {
			continue
		}
		command, ok := testCommand(commandFromPayload(event.Payload))
		if !ok {
			continue
		}
		success, known := commandSucceeded(event.Payload)
		if !known {
			continue
		}
		scope, target := classifyScope(command)
		runs = append(runs, testRun{
			at: parseTime(event.TS), command: command, scope: scope, target: target, success: success,
			source: SourceReference{SourceType: "event", SourceID: event.ID, Excerpt: bounded(command)},
		})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].at.Equal(runs[j].at) {
			return runs[i].source.SourceID < runs[j].source.SourceID
		}
		return runs[i].at.Before(runs[j].at)
	})
	return runs
}

type failure struct {
	at      time.Time
	command string
	source  SourceReference
}

func collectFailures(input Input) []failure {
	var failures []failure
	for _, sig := range input.Failures {
		if !matchesInput(sig.ProjectID, sig.SessionID, input) || sig.ResolvedTS != "" || sig.RepeatCount < 2 {
			continue
		}
		command, ok := testCommand(sig.CommandFamily)
		if !ok {
			continue
		}
		failures = append(failures, failure{
			at: parseTime(sig.LastSeenTS), command: command,
			source: SourceReference{SourceType: "failure_signature", SourceID: sig.ID, Excerpt: bounded(sig.NormalizedMessage)},
		})
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].at.Before(failures[j].at) })
	return failures
}

func matchTests(changed change, runs []testRun) (successes []testRun, counters []testRun) {
	for _, run := range runs {
		if !run.at.After(changed.at) || run.at.Sub(changed.at) > delayedTestWindow {
			counters = append(counters, run)
			continue
		}
		relevant := isRelevant(changed.path, run)
		if relevant && run.success {
			successes = append(successes, run)
		} else {
			counters = append(counters, run)
		}
	}
	sort.SliceStable(successes, func(i, j int) bool {
		left, right := verificationConfidence(successes[i], changed), verificationConfidence(successes[j], changed)
		if left == right {
			return successes[i].at.Before(successes[j].at)
		}
		return left > right
	})
	return successes, counters
}

func failuresAfter(changed change, failures []failure) []failure {
	var matches []failure
	for _, failure := range failures {
		if failure.at.After(changed.at) && failure.at.Sub(changed.at) <= delayedTestWindow {
			matches = append(matches, failure)
		}
	}
	return matches
}

func successesBefore(runs []testRun, at time.Time) []testRun {
	var matched []testRun
	for _, run := range runs {
		if run.at.Before(at) {
			matched = append(matched, run)
		}
	}
	return matched
}

func verificationConfidence(run testRun, changed change) float64 {
	confidence := broadVerificationConfidence
	if run.scope == targetedScope {
		confidence = targetedVerificationConfidence
	}
	if run.at.Sub(changed.at) > 5*time.Minute {
		confidence -= delayedConfidenceReduction
	}
	return confidence
}

func isRelevant(path string, run testRun) bool {
	if run.scope == broadScope {
		return true
	}
	if path == "" || run.target == "" {
		return false
	}
	path, target := cleanPath(path), cleanPath(run.target)
	pathDir := filepath.Dir(path)
	return target == path || target == pathDir || strings.HasPrefix(path, target+"/") || strings.HasPrefix(target, pathDir+"/")
}

func isToolEvent(event db.Event) bool {
	return strings.EqualFold(event.EventType, "PostToolUse")
}

func isWriteEvent(event db.Event) bool {
	if !isToolEvent(event) {
		return false
	}
	name := strings.ToLower(event.ToolName)
	return strings.Contains(name, "write") || strings.Contains(name, "edit") || strings.Contains(name, "patch")
}

func isFormattingEvent(event db.Event) bool {
	name := strings.ToLower(event.ToolName)
	return strings.Contains(name, "format") || strings.Contains(name, "gofmt") || strings.Contains(name, "prettier")
}

func isMeaningfulCodePath(path string) bool {
	p := strings.ToLower(cleanPath(path))
	if p == "" || strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "doc/") || strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".txt") || strings.HasSuffix(p, ".lock") || strings.HasSuffix(p, ".sum") {
		return false
	}
	base := filepath.Base(p)
	if base == "readme" || strings.HasPrefix(base, "readme.") || base == "license" || strings.HasPrefix(base, "license.") || base == "changelog" || strings.HasPrefix(base, "changelog.") || base == ".gitignore" || base == "package-lock.json" || base == "pnpm-lock.yaml" || base == "yarn.lock" {
		return false
	}
	if strings.Contains(p, "/vendor/") || strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/generated/") || strings.HasPrefix(p, "generated/") || strings.Contains(base, "generated") || strings.Contains(base, ".gen.") {
		return false
	}
	return !strings.Contains(base, "_test.") && !strings.Contains(base, ".test.")
}

func commandFromPayload(payload string) string {
	var value any
	if json.Unmarshal([]byte(payload), &value) == nil {
		if command := nestedString(value, "command"); command != "" {
			return command
		}
	}
	return payload
}

func filePath(payload string) string {
	var value any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return ""
	}
	for _, key := range []string{"file_path", "filePath", "path"} {
		if path := nestedString(value, key); path != "" {
			return path
		}
	}
	return ""
}

func isWhitespaceOnlyEdit(payload string) bool {
	var value any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return false
	}
	oldText, newText := nestedString(value, "old_string"), nestedString(value, "new_string")
	if oldText == "" || newText == "" || oldText == newText {
		return false
	}
	return removeWhitespace(oldText) == removeWhitespace(newText)
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func nestedString(value any, key string) string {
	switch node := value.(type) {
	case map[string]any:
		if text, ok := node[key].(string); ok {
			return strings.TrimSpace(text)
		}
		for _, child := range node {
			if found := nestedString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range node {
			if found := nestedString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func commandSucceeded(payload string) (bool, bool) {
	var value any
	if json.Unmarshal([]byte(payload), &value) == nil {
		if code, found := nestedExitCode(value); found {
			return code == 0, true
		}
		output := strings.ToLower(strings.Join(responseStrings(value), "\n"))
		if hasFailureText(output) {
			return false, true
		}
		if hasSuccessText(output) {
			return true, true
		}
		return false, false
	}
	text := strings.ToLower(payload)
	if hasFailureText(text) {
		return false, true
	}
	if hasSuccessText(text) {
		return true, true
	}
	return false, false
}

func nestedExitCode(value any) (int, bool) {
	switch node := value.(type) {
	case map[string]any:
		if raw, ok := node["exit_code"]; ok {
			switch code := raw.(type) {
			case float64:
				return int(code), true
			case string:
				if strings.TrimSpace(code) == "0" {
					return 0, true
				}
				return 1, true
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

func responseStrings(value any) []string {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	response, ok := root["tool_response"]
	if !ok {
		return nil
	}
	var stringsOut []string
	collectStrings(response, &stringsOut)
	return stringsOut
}

func collectStrings(value any, out *[]string) {
	switch node := value.(type) {
	case string:
		*out = append(*out, node)
	case map[string]any:
		for _, child := range node {
			collectStrings(child, out)
		}
	case []any:
		for _, child := range node {
			collectStrings(child, out)
		}
	}
}

func hasFailureText(text string) bool {
	return strings.Contains(text, "fail") || strings.Contains(text, "error") || strings.Contains(text, "exception")
}

func hasSuccessText(text string) bool {
	return strings.Contains(text, "pass") || strings.Contains(text, "success") || strings.Contains(text, "ok\t") || strings.Contains(text, "ok ")
}

func testCommand(raw string) (string, bool) {
	for _, segment := range commandSegments(raw) {
		lower := strings.ToLower(segment)
		switch {
		case strings.HasPrefix(lower, "go test"), strings.HasPrefix(lower, "cargo test"), strings.HasPrefix(lower, "pytest"),
			strings.HasPrefix(lower, "npm test"), strings.HasPrefix(lower, "npm run test"), strings.HasPrefix(lower, "pnpm test"),
			strings.HasPrefix(lower, "yarn test"), strings.HasPrefix(lower, "bun test"), strings.HasPrefix(lower, "jest"), strings.HasPrefix(lower, "vitest"),
			strings.HasPrefix(lower, "npx jest"), strings.HasPrefix(lower, "npx vitest"), strings.HasPrefix(lower, "python -m pytest"),
			strings.HasPrefix(lower, "python3 -m pytest"), strings.HasPrefix(lower, "python -m unittest"), strings.HasPrefix(lower, "python3 -m unittest"),
			strings.HasPrefix(lower, "mvn test"), strings.HasPrefix(lower, "gradle test"), strings.HasPrefix(lower, "./mvnw test"),
			strings.HasPrefix(lower, "./gradlew test"), strings.HasPrefix(lower, "rspec"), strings.HasPrefix(lower, "bundle exec rspec"),
			strings.HasPrefix(lower, "dotnet test"):
			return strings.TrimSpace(segment), true
		}
	}
	return "", false
}

func commandSegments(command string) []string {
	command = strings.ReplaceAll(command, "\n", ";")
	command = strings.ReplaceAll(command, "&&", ";")
	command = strings.ReplaceAll(command, "||", ";")
	return strings.Split(command, ";")
}

func classifyScope(command string) (testScope, string) {
	lower := strings.ToLower(command)
	if strings.Contains(lower, "./...") || strings.Contains(lower, " --all") || strings.Contains(lower, " -a") {
		return broadScope, ""
	}
	parts := strings.Fields(command)
	start := testArgumentStart(parts)
	for _, part := range parts[start:] {
		if strings.HasPrefix(part, "-") || part == "--" || part == "test" || part == "run" || part == "exec" {
			continue
		}
		if strings.Contains(part, "/") || strings.Contains(part, "\\") || strings.HasSuffix(part, ".csproj") || strings.HasSuffix(part, ".rb") {
			return targetedScope, part
		}
	}
	return broadScope, ""
}

func testArgumentStart(parts []string) int {
	for i, part := range parts {
		if part == "test" || part == "rspec" || part == "jest" || part == "vitest" || part == "pytest" || part == "unittest" {
			return i + 1
		}
	}
	return len(parts)
}

func sameTestFamily(left, right string) bool {
	leftFields, rightFields := strings.Fields(strings.ToLower(left)), strings.Fields(strings.ToLower(right))
	if len(leftFields) == 0 || len(rightFields) == 0 {
		return false
	}
	return leftFields[0] == rightFields[0]
}

func verificationSummary(run testRun) string {
	if run.scope == targetedScope {
		return "A targeted test passed after a code change."
	}
	return "A project-wide test suite passed after a code change."
}

func testSources(runs []testRun) []SourceReference {
	refs := make([]SourceReference, 0, len(runs))
	for _, run := range runs {
		refs = append(refs, run.source)
	}
	return refs
}

func matchesInput(projectID, sessionID string, input Input) bool {
	return (input.ProjectID == "" || input.ProjectID == projectID) && (input.SessionID == "" || input.SessionID == sessionID)
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func sortChanges(changes []change) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].at.Equal(changes[j].at) {
			return changes[i].source.SourceID < changes[j].source.SourceID
		}
		return changes[i].at.Before(changes[j].at)
	})
}

func cleanPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
}

func bounded(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}
