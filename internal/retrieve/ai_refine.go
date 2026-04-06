package retrieve

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

type aiHintDecision struct {
	Relevant   bool    `json:"relevant"`
	Confidence float64 `json:"confidence"`
	Hint       string  `json:"hint"`
}

func maybeRefineContext7Hint(job *db.RetrievalJob, hint string, parts []string) (string, bool, error) {
	if !shouldUseAIHintRefinement(job, hint, parts) {
		return hint, false, nil
	}
	cfg, ok := loadAIHintConfig()
	if !ok {
		return hint, false, nil
	}

	response, err := distill.CompletePrompt(cfg, buildAIHintPrompt(job, hint, parts))
	if err != nil {
		return hint, false, err
	}
	decision, err := parseAIHintDecision(response)
	if err != nil {
		return hint, false, err
	}
	refined := cleanContext7Sentence(decision.Hint)
	if !decision.Relevant || decision.Confidence < 0.75 || refined == "" {
		return hint, false, nil
	}
	return truncate(ensureSentence(refined), 160), true, nil
}

func shouldUseAIHintRefinement(job *db.RetrievalJob, hint string, parts []string) bool {
	if job == nil || strings.TrimSpace(hint) == "" || len(parts) == 0 {
		return false
	}
	return job.Source == "context7" && job.TriggerType == "failure"
}

func loadAIHintConfig() (distill.Config, bool) {
	if !distill.HasExplicitProviderConfig() {
		return distill.Config{}, false
	}
	cfg := distill.LoadConfig()
	switch cfg.Provider {
	case distill.ProviderOpenAI, distill.ProviderAnthropic, "forge":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return distill.Config{}, false
		}
	case distill.ProviderCodex:
		return cfg, true
	case distill.ProviderOllama:
		explicitProvider := strings.TrimSpace(strings.ToLower(os.Getenv("FORGE_PROVIDER")))
		if explicitProvider == "" {
			fileCfg, err := config.Load()
			if err == nil {
				explicitProvider = strings.TrimSpace(strings.ToLower(fileCfg.Provider))
			}
		}
		if explicitProvider != string(distill.ProviderOllama) {
			return distill.Config{}, false
		}
	default:
		return distill.Config{}, false
	}
	return cfg, true
}

func buildAIHintPrompt(job *db.RetrievalJob, hint string, parts []string) string {
	excerpts := topContext7Excerpts(parts, 5)
	var sb strings.Builder
	sb.WriteString("You validate official library documentation for an AI coding assistant.\n")
	sb.WriteString("Decide whether the docs excerpts clearly support a better one-sentence remediation hint for the failure.\n")
	sb.WriteString("Return ONLY JSON with keys relevant (boolean), confidence (0..1), hint (string).\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Use only the provided docs excerpts.\n")
	sb.WriteString("- If the docs do not clearly answer the failure, return relevant=false and hint=\"\".\n")
	sb.WriteString("- If relevant=true, hint must be one sentence, actionable, and at most 160 characters.\n")
	sb.WriteString("- Do not mention Context7, docs, or sources.\n\n")
	sb.WriteString("Failure context:\n")
	sb.WriteString("library: " + firstNonEmpty(job.LibraryName, "unknown") + "\n")
	sb.WriteString("query: " + compactQuery(job.Query) + "\n")
	sb.WriteString("current_hint: " + cleanContext7Sentence(hint) + "\n\n")
	sb.WriteString("Docs excerpts:\n")
	for i, excerpt := range excerpts {
		sb.WriteString(strings.TrimSpace(ensureSentence(strconv.Itoa(i+1) + ". " + excerpt)))
		sb.WriteString("\n")
	}
	return sb.String()
}

func topContext7Excerpts(parts []string, limit int) []string {
	candidates := context7SentenceCandidates(parts)
	if len(candidates) == 0 {
		candidates = parts
	}
	var excerpts []string
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		candidate = cleanContext7Sentence(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		excerpts = append(excerpts, truncate(candidate, 220))
		if len(excerpts) == limit {
			break
		}
	}
	return excerpts
}

func parseAIHintDecision(response string) (aiHintDecision, error) {
	response = strings.TrimSpace(response)
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start >= 0 && end > start {
		response = response[start : end+1]
	}
	var decision aiHintDecision
	err := json.Unmarshal([]byte(response), &decision)
	return decision, err
}

func aiSummaryKind(base string, refined bool) string {
	if !refined {
		return base
	}
	if base == "" {
		return "ai_refined"
	}
	return base + "_ai"
}

func aiSummaryTags(tags []string, refined bool) []string {
	if !refined {
		return tags
	}
	for _, tag := range tags {
		if tag == "ai_refined" {
			return tags
		}
	}
	return append(tags, "ai_refined")
}
