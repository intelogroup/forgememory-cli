package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/tokens"
)

func isFirstPromptOfSession(database *db.DB, sessionID string) bool {
	if sessionID == "" || sessionID == "unknown" {
		return false
	}
	events, err := database.SessionEvents(sessionID, 1)
	return err == nil && len(events) == 0
}

// loadLastProjectSession returns the most recent session summary for a project,
// excluding the current session (which has not been synthesized yet).
func loadLastProjectSession(database *db.DB, projectID, currentSessionID string) *db.SessionSummary {
	summaries, err := database.GetRecentSessionSummariesByProject(projectID, 5)
	if err != nil {
		return nil
	}
	for i := range summaries {
		if summaries[i].SessionID != currentSessionID {
			return &summaries[i]
		}
	}
	return nil
}

func loadSessionRecallContext(database *db.DB, projectID, promptText string) ([]db.Principle, []db.SessionSummary, []db.Alert, []db.ExternalContextSummary, *promptRecallMatch, string) {
	principles, _ := database.RecentPrinciplesByProject(projectID, 2)

	// Load more summaries than needed and score them against the current prompt
	// for relevance, so the most contextually relevant lessons are injected.
	tokens := recallTokens(promptText)
	summaries := loadRankedSummaries(database, projectID, tokens, 10)

	alerts, _ := database.ActiveAlertsByProject(projectID, 2)
	externalSummaries, _ := database.FreshExternalContextSummariesByProject(projectID, 2)
	if projectID != "" && len(principles) == 0 && len(summaries) == 0 {
		principles, _ = database.RecentPrinciplesByProject("", 2)
		summaries = loadRankedSummaries(database, "", tokens, 10)
	}

	distillError := ""
	if h, err := database.GetDistillationHealth(); err == nil && h.LastStatus == "failed" {
		distillError = h.LastErrorMessage
	}

	return principles, summaries, alerts, externalSummaries, findBestPromptRecall(database, projectID, promptText), distillError
}

// loadRankedSummaries loads recent session summaries and returns the top n
// ranked by prompt-token relevance, falling back to most recent if no prompt
// tokens are available.
func loadRankedSummaries(database *db.DB, projectID string, tokens []string, n int) []db.SessionSummary {
	loadLimit := n * 3
	var summaries []db.SessionSummary
	var err error
	if projectID != "" {
		summaries, err = database.GetRecentSessionSummariesByProject(projectID, loadLimit)
	} else {
		summaries, err = database.GetRecentSessionSummaries(loadLimit)
	}
	if err != nil || len(summaries) == 0 {
		return summaries
	}

	// If few or no tokens, fall back to most recent.
	if len(tokens) < 2 {
		if len(summaries) > n {
			summaries = summaries[:n]
		}
		return summaries
	}

	type scored struct {
		summary db.SessionSummary
		score   float64
	}
	var scoredList []scored
	for _, s := range summaries {
		if match, ok := scorePromptSessionSummaryMatch(s, tokens); ok {
			scoredList = append(scoredList, scored{s, match.Score})
		} else {
			scoredList = append(scoredList, scored{s, 0})
		}
	}

	sort.SliceStable(scoredList, func(i, j int) bool {
		if scoredList[i].score == scoredList[j].score {
			return scoredList[i].summary.TS > scoredList[j].summary.TS
		}
		return scoredList[i].score > scoredList[j].score
	})

	out := make([]db.SessionSummary, 0, n)
	for i := 0; i < len(scoredList) && i < n; i++ {
		out = append(out, scoredList[i].summary)
	}
	if len(out) == 0 && len(summaries) > 0 {
		if len(summaries) > n {
			summaries = summaries[:n]
		}
		return summaries
	}
	return out
}

func shouldWaitForOfficialHint(alerts []db.Alert, externalSummaries []db.ExternalContextSummary) bool {
	for _, summary := range externalSummaries {
		if summary.Source == "context7" &&
			summary.SummaryKind != "docs_summary" &&
			strings.TrimSpace(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title)) != "" {
			return false
		}
	}
	for _, alert := range alerts {
		if alert.AlertType == "repeated_failure" {
			return true
		}
	}
	return false
}

func hasPendingFailureRetrieval(database *db.DB, projectID string) bool {
	if database == nil || strings.TrimSpace(projectID) == "" {
		return false
	}
	jobs, err := database.RetrievalJobsByProject(projectID, 6)
	if err != nil {
		return false
	}
	for _, job := range jobs {
		if job.TriggerType != "failure" || job.Source != "context7" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func waitForOfficialHint(database *db.DB, projectID string) {
	if database == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	deadline := time.Now().Add(recallWaitBudget())
	poll := recallPollInterval()
	for time.Now().Before(deadline) {
		summaries, err := database.FreshExternalContextSummariesByProject(projectID, 2)
		if err == nil {
			for _, summary := range summaries {
				if summary.Source == "context7" &&
					summary.SummaryKind != "docs_summary" &&
					strings.TrimSpace(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title)) != "" {
					return
				}
			}
		}
		if !hasPendingFailureRetrieval(database, projectID) {
			return
		}
		time.Sleep(poll)
	}
}

func recallWaitBudget() time.Duration {
	ms := envInt("FORGE_HOOK_RECALL_WAIT_MS", 2500)
	if ms < 0 {
		ms = 0
	}
	if ms > 5000 {
		ms = 5000
	}
	return time.Duration(ms) * time.Millisecond
}

func recallPollInterval() time.Duration {
	ms := envInt("FORGE_HOOK_RECALL_POLL_MS", 100)
	if ms < 25 {
		ms = 25
	}
	if ms > 250 {
		ms = 250
	}
	return time.Duration(ms) * time.Millisecond
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

// spawnSessionSynthesis detaches a `forge synthesize-session` process so the
// hook can return immediately without blocking on an LLM call.
func buildSessionRecallOutput(projectID string, summaries []db.SessionSummary, principles []db.Principle, alerts []db.Alert, externalSummaries []db.ExternalContextSummary, promptMatch *promptRecallMatch, lastSession *db.SessionSummary, distillError string) string {
	var sentences []string

	matchThreshold := 2.0
	if promptMatch != nil && promptMatch.ImplHint != "" {
		matchThreshold = 1.5
	}
	if promptMatch != nil && promptMatch.Score >= matchThreshold {
		source := displayProjectName(promptMatch.ProjectID)
		if promptMatch.ImplHint != "" {
			outcome := promptMatch.Outcome
			label := "Past " + outcome
			if outcome == "" || outcome == "unknown" {
				label = "Prior"
			}
			title := promptMatch.Title
			if title == "" {
				title = promptMatch.SourceType
			}
			sentences = append(sentences, fmt.Sprintf(
				"%s [%s]: %s — %s.",
				label,
				source,
				title,
				promptMatch.ImplHint,
			))
		} else {
			sentences = append(sentences, fmt.Sprintf(
				"Prompt-matched %s from %s (%s confidence): %s.",
				promptMatch.SourceType,
				source,
				confidenceLabel(promptMatch.Score),
				trimSentence(promptMatch.Narrative),
			))
		}
	}

	// Docs/API insights
	hasOfficialHint := false
	for _, summary := range externalSummaries {
		label := summary.Source
		if summary.LibraryName != "" && summary.LibraryName != summary.Source {
			label = summary.Source + " " + summary.LibraryName
		}
		summaryText := trimSentence(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title))
		if summary.Source == "context7" && summary.SummaryKind != "docs_summary" {
			hasOfficialHint = true
			sentences = append(sentences, fmt.Sprintf("Official docs hint from %s: %s.", label, summaryText))
		} else {
			sentences = append(sentences, fmt.Sprintf("Cached docs insight from %s: %s.", label, summaryText))
		}
		break
	}

	if !hasOfficialHint {
		for _, alert := range alerts {
			if alert.AlertType != "repeated_failure" {
				continue
			}
			sentences = append(sentences, fmt.Sprintf("Active repeated failure: %s.", trimSentence(firstNonEmpty(alert.Narrative, alert.Title))))
			break
		}
	}

	if distillError != "" && len(sentences) > 0 {
		errMsg := distillError
		if len(errMsg) > 80 {
			errMsg = errMsg[:80] + "..."
		}
		sentences = append([]string{fmt.Sprintf("[System: Memory layer degraded. Distillation failing: %s. Run 'forge health' to diagnose.]", errMsg)}, sentences...)
	}

	if len(sentences) == 0 {
		return ""
	}

	return "<forge-context>\n## Forge Context\n" + strings.Join(sentences, " ") + "\n</forge-context>"
}

func extractPromptText(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ""
	}

	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return compactWhitespace(payload)
	}

	var candidates []promptCandidate
	collectPromptCandidates(parsed, "", &candidates)
	if len(candidates) == 0 {
		return compactWhitespace(payload)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	var merged []string
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		text := compactWhitespace(candidate.text)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		merged = append(merged, text)
		if len(merged) == 3 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(merged, " "))
}

func collectPromptCandidates(node any, parentKey string, out *[]promptCandidate) {
	switch value := node.(type) {
	case map[string]any:
		role, _ := value["role"].(string)
		role = strings.TrimSpace(role)
		if strings.EqualFold(role, "user") {
			for _, key := range []string{"content", "text", "message", "input"} {
				if text := flattenPromptValue(value[key]); text != "" {
					*out = append(*out, promptCandidate{priority: 0, text: text})
				}
			}
		}

		roleScoped := role != ""
		if !roleScoped || strings.EqualFold(role, "user") {
			for key, priority := range promptFieldPriority {
				if text := flattenPromptValue(value[key]); text != "" {
					*out = append(*out, promptCandidate{priority: priority, text: text})
				}
			}
		}
		for key, child := range value {
			if roleScoped && !strings.EqualFold(role, "user") {
				if _, skip := promptFieldPriority[key]; skip {
					continue
				}
			}
			collectPromptCandidates(child, key, out)
		}
	case []any:
		for _, child := range value {
			collectPromptCandidates(child, parentKey, out)
		}
	case string:
		if _, ok := promptFieldPriority[parentKey]; ok {
			*out = append(*out, promptCandidate{priority: promptFieldPriority[parentKey], text: value})
		}
	}
}

func flattenPromptValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if text := flattenPromptValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		var parts []string
		for _, key := range []string{"text", "content", "message", "prompt", "input"} {
			if text := flattenPromptValue(v[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func findBestPromptRecall(database *db.DB, projectID, promptText string) *promptRecallMatch {
	tokens := recallTokens(promptText)
	if len(tokens) < 2 {
		return nil
	}

	var matches []promptRecallMatch
	matches = append(matches, findPromptRelevantPrinciples(database, projectID, tokens)...)
	matches = append(matches, findPromptRelevantSessionSummaries(database, projectID, tokens)...)
	if len(matches) == 0 {
		return nil
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].TS > matches[j].TS
		}
		return matches[i].Score > matches[j].Score
	})
	return &matches[0]
}

func findPromptRelevantPrinciples(database *db.DB, projectID string, tokens []string) []promptRecallMatch {
	// Scan same-project principles first (cheaper, higher signal). If we find a
	// high-confidence match there, skip the cross-project scan entirely.
	var matches []promptRecallMatch
	seen := make(map[string]bool)

	if projectID != "" {
		same, err := database.RecentPrinciplesByProject(projectID, 50)
		if err == nil {
			for _, p := range same {
				match, ok := scorePromptPrincipleMatch(p, tokens)
				if !ok || seen[p.Fingerprint] {
					continue
				}
				seen[p.Fingerprint] = true
				matches = append(matches, match)
			}
		}
		// If we already have a high-confidence same-project hit, skip cross-project.
		// Principles with a concrete impl_hint are promoted at a lower threshold (1.5).
		for _, m := range matches {
			threshold := 2.0
			if m.ImplHint != "" {
				threshold = 1.5
			}
			if m.Score >= threshold {
				sort.SliceStable(matches, func(i, j int) bool {
					if matches[i].Score == matches[j].Score {
						return matches[i].TS > matches[j].TS
					}
					return matches[i].Score > matches[j].Score
				})
				return matches
			}
		}
	}

	// Cross-project scan — active only, capped at 100.
	cross, err := database.RecentActivePrinciples(100)
	if err != nil {
		return matches
	}
	for _, p := range cross {
		if sameProject(projectID, p.ProjectID) {
			continue
		}
		match, ok := scorePromptPrincipleMatch(p, tokens)
		if !ok || seen[p.Fingerprint] {
			continue
		}
		seen[p.Fingerprint] = true
		matches = append(matches, match)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].TS > matches[j].TS
		}
		return matches[i].Score > matches[j].Score
	})
	return matches
}

func scorePromptPrincipleMatch(principle db.Principle, tokens []string) (promptRecallMatch, bool) {
	score, matched, ok := scorePromptMatch(strings.Join([]string{
		principle.Title,
		principle.Narrative,
		strings.Join(principle.Concepts, " "),
		strings.Join(principle.FilesModified, " "),
	}, " "), principle.TS, tokens, principle.ImpactScore, 1.8, 1.2)
	if !ok {
		return promptRecallMatch{}, false
	}

	return promptRecallMatch{
		SourceType:   "principle",
		ProjectID:    principle.ProjectID,
		Narrative:    principle.Narrative,
		Title:        principle.Title,
		TS:           principle.TS,
		Score:        score,
		MatchedTerms: matched,
		Outcome:      principle.Outcome,
		ImplHint:     principle.ImplHint,
	}, true
}

func findPromptRelevantSessionSummaries(database *db.DB, projectID string, tokens []string) []promptRecallMatch {
	var matches []promptRecallMatch
	seen := make(map[string]bool)

	if projectID != "" {
		same, err := database.GetRecentSessionSummariesByProject(projectID, 20)
		if err == nil {
			for _, s := range same {
				match, ok := scorePromptSessionSummaryMatch(s, tokens)
				if !ok || seen[s.SessionID] {
					continue
				}
				seen[s.SessionID] = true
				matches = append(matches, match)
			}
		}
		for _, m := range matches {
			if m.Score >= 2.0 {
				return matches
			}
		}
	}

	// Cross-project scan — capped at 80 most recent summaries.
	cross, err := database.GetRecentSessionSummaries(80)
	if err != nil {
		return matches
	}
	for _, s := range cross {
		if sameProject(projectID, s.ProjectID) {
			continue
		}
		match, ok := scorePromptSessionSummaryMatch(s, tokens)
		if !ok || seen[s.SessionID] {
			continue
		}
		seen[s.SessionID] = true
		matches = append(matches, match)
	}
	return matches
}

func scorePromptSessionSummaryMatch(summary db.SessionSummary, tokens []string) (promptRecallMatch, bool) {
	narrative := firstNonEmpty(summary.Learnings, summary.Investigation, summary.NextSteps, summary.Summary, summary.Request)
	if strings.TrimSpace(narrative) == "" {
		return promptRecallMatch{}, false
	}

	quality := 0.55
	if strings.TrimSpace(summary.Learnings) != "" {
		quality = 0.78
	} else if strings.TrimSpace(summary.Investigation) != "" {
		quality = 0.68
	}

	score, matched, ok := scorePromptMatch(strings.Join([]string{
		summary.Request,
		summary.Investigation,
		summary.Learnings,
		summary.NextSteps,
		summary.Summary,
		summary.Keywords,
	}, " "), summary.TS, tokens, quality, 1.55, 1.15)
	if !ok {
		return promptRecallMatch{}, false
	}

	return promptRecallMatch{
		SourceType:   "session lesson",
		ProjectID:    summary.ProjectID,
		Narrative:    narrative,
		TS:           summary.TS,
		Score:        score,
		MatchedTerms: matched,
	}, true
}

func scorePromptMatch(haystack, ts string, tokens []string, quality, overlapWeight, threshold float64) (float64, []string, bool) {
	haystackTokens := tokenSet(haystack)
	var matched []string
	for _, token := range tokens {
		if haystackTokens[token] {
			matched = append(matched, token)
		}
	}

	if len(matched) == 0 {
		return 0, nil, false
	}
	if len(matched) < 2 {
		if len(matched[0]) < 8 || quality < 0.75 {
			return 0, nil, false
		}
	}

	focus := len(tokens)
	if focus > 4 {
		focus = 4
	}
	if focus == 0 {
		return 0, nil, false
	}

	score := (float64(len(matched)) / float64(focus)) * overlapWeight
	score += quality * 0.9
	score += recentnessScore(ts)
	if score < threshold {
		return 0, nil, false
	}
	return score, matched, true
}

func recentnessScore(ts string) float64 {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	age := time.Since(parsed)
	switch {
	case age <= 7*24*time.Hour:
		return 0.35
	case age <= 30*24*time.Hour:
		return 0.2
	case age <= 180*24*time.Hour:
		return 0.1
	default:
		return 0
	}
}

func recallTokens(text string) []string {
	return tokens.Tokenize(text, recallStopWords)
}

func tokenSet(text string) map[string]bool {
	return tokens.TokenSet(text, recallStopWords)
}

func sortedKeys(set map[string]bool) []string {
	return tokens.SortedKeys(set)
}
