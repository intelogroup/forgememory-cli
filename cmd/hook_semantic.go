package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/embed"
	"github.com/forge/forge/internal/recall"
	"github.com/forge/forge/internal/sanitize"
)

const (
	// semanticFloor keeps injection precise, because vector search always returns
	// something. Measured on 150 real first prompts with mxbai-embed-large: 0.72
	// fired on 14% and every sampled hit was on topic; 0.70 let junk through.
	// shortcut: tuned for that model only, re-measure if the default model changes.
	semanticFloor = 0.72

	maxSemanticLessons = 3
	semanticCandidates = 10
	maxSemanticQuery   = 2000
	maxLessonBody      = 200

	// semanticBudget bounds the wait on the first prompt. A cold model outlasts it;
	// the keyword match still shows and the load finishes for the next session.
	semanticBudget = 2500 * time.Millisecond
)

// firstPromptLessons finds past lessons related to the prompt by meaning. Any
// failure returns nothing: the keyword match already in the hook is the fallback.
func firstPromptLessons(database *db.DB, projectID, prompt string) []recall.Result {
	client, err := embed.FromEnv()
	if err != nil {
		return nil
	}
	return semanticPromptLessons(context.Background(), database, client, projectID, currentProjectKey(), prompt)
}

func semanticPromptLessons(ctx context.Context, database *db.DB, e recall.Embedder, projectID, projectKey, prompt string) []recall.Result {
	if len(recallTokens(prompt)) < 3 {
		return nil
	}
	if len(prompt) > maxSemanticQuery {
		prompt = strings.ToValidUTF8(prompt[:maxSemanticQuery], "")
	}
	ctx, cancel := context.WithTimeout(ctx, semanticBudget)
	defer cancel()
	resp, err := recall.Search(ctx, database, e, prompt, db.RecallScope{ProjectID: projectID, ProjectKey: projectKey}, semanticCandidates)
	if err != nil || resp.Mode != recall.ModeHybrid {
		return nil
	}
	var lessons []recall.Result
	for _, r := range resp.Results {
		if r.Kind == db.RecallKindPrinciple && r.Similarity >= semanticFloor {
			lessons = append(lessons, r)
		}
	}
	sort.SliceStable(lessons, func(i, j int) bool { return lessons[i].Similarity > lessons[j].Similarity })
	if len(lessons) > maxSemanticLessons {
		lessons = lessons[:maxSemanticLessons]
	}
	return lessons
}

// appendSemanticLessons adds a block for lessons the text does not already show,
// and returns the ones it added.
func appendSemanticLessons(text string, lessons []recall.Result) (string, []recall.Result) {
	var lines []string
	var shown []recall.Result
	for _, l := range lessons {
		title := cleanLessonText(l.Title)
		if title == "" || strings.Contains(text, title) {
			continue
		}
		line := "- " + title
		if l.Partial {
			line += " (title only, unverified)"
		} else if body := cleanLessonText(l.Body); body != "" && body != title {
			line += ": " + clipRunes(body, maxLessonBody)
		}
		if l.ProjectID != "" {
			line += " [" + displayProjectName(l.ProjectID) + "]"
		}
		lines = append(lines, line)
		shown = append(shown, l)
	}
	if len(lines) == 0 {
		return text, nil
	}
	block := "<forge-context>\n## Related past lessons\n" + strings.Join(lines, "\n") + "\n</forge-context>"
	if strings.TrimSpace(text) == "" {
		return block, shown
	}
	return text + "\n\n" + block, shown
}

// cleanLessonText flattens stored text to one line and stops it closing the block.
func cleanLessonText(s string) string {
	s = strings.NewReplacer("<forge-context>", "", "</forge-context>", "").Replace(s)
	return sanitize.ScrubSecrets(strings.Join(strings.Fields(s), " "))
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
