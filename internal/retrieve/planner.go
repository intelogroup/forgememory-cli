package retrieve

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/forge/forge/internal/db"
)

type candidate struct {
	source      string
	libraryName string
	query       string
	priority    int
}

var docsIntentTerms = map[string]bool{
	"api": true, "apis": true, "compile": true, "compiler": true, "crate": true,
	"dependency": true, "dependencies": true, "docs": true, "documentation": true,
	"error": true, "errors": true, "failing": true, "failure": true, "guide": true,
	"import": true, "install": true, "installation": true, "local": true, "mcp": true,
	"official": true, "package": true, "packages": true, "reference": true,
	"references": true, "setup": true, "sdk": true, "syntax": true, "undefined": true,
}

var setupIntentTerms = map[string]bool{
	"install": true, "installation": true, "local": true, "mcp": true, "setup": true,
}

var sourceIntentTerms = map[string]bool{
	"implementation": true, "implementations": true, "internals": true,
	"source": true, "under": true,
}

var stackAliases = map[string]string{
	"rust": "rust", "cargo": "rust", "tokio": "tokio", "axum": "axum", "actix": "actix",
	"tauri": "tauri", "next": "next.js", "nextjs": "next.js", "next.js": "next.js",
	"react": "react", "firebase": "firebase", "vercel": "vercel", "supabase": "supabase",
	"prisma": "prisma", "tailwind": "tailwindcss", "tailwindcss": "tailwindcss",
	"typescript": "typescript", "javascript": "javascript", "node": "node.js", "nodejs": "node.js",
	"vite": "vite", "astro": "astro", "nuxt": "nuxt", "svelte": "svelte", "vue": "vue",
	"context7": "context7",
	"postgres": "postgresql", "postgresql": "postgresql",
}

var opensrcLibraries = map[string]bool{
	"next.js": true, "react": true, "firebase": true, "prisma": true, "tailwindcss": true,
	"node.js": true, "vite": true, "astro": true, "nuxt": true, "svelte": true, "vue": true,
}

// EnqueuePromptRetrieval plans external retrieval for stack/docs-oriented prompts.
func EnqueuePromptRetrieval(database *db.DB, projectID, promptText string) error {
	if database == nil {
		return nil
	}
	plans := planPrompt(promptText)
	for _, plan := range plans {
		if err := database.InsertRetrievalJob(&db.RetrievalJob{
			ProjectID:   projectID,
			TriggerType: "prompt",
			Source:      plan.source,
			Query:       plan.query,
			LibraryName: plan.libraryName,
			Status:      "queued",
			Priority:    plan.priority,
			DedupeKey:   dedupeKey(projectID, plan.source, plan.libraryName, plan.query),
		}); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueFailureRetrieval plans official-doc retrieval from a repeated failure
// signature so the next prompt can receive a compact docs hint.
func EnqueueFailureRetrieval(database *db.DB, projectID, commandFamily, errorKind, normalizedMessage, payload string) error {
	if database == nil {
		return nil
	}
	library := inferFailureLibrary(commandFamily, errorKind, normalizedMessage, payload)
	if library == "" {
		return nil
	}
	query := buildFailureQuery(library, commandFamily, errorKind, normalizedMessage)
	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   projectID,
		TriggerType: "failure",
		Source:      "context7",
		Query:       query,
		LibraryName: library,
		Status:      "queued",
		Priority:    100,
		DedupeKey:   dedupeKey(projectID, "context7", library, query),
	}); err != nil {
		return err
	}
	RequestImmediateWake()
	return nil
}

func planPrompt(promptText string) []candidate {
	tokens := tokenize(promptText)
	if len(tokens) == 0 {
		return nil
	}

	docsIntent := hasAnyToken(tokens, docsIntentTerms) || strings.Contains(strings.ToLower(promptText), "error[")
	if !docsIntent {
		return nil
	}

	libraries := detectLibraries(tokens)
	var plans []candidate
	if isContext7SetupPrompt(tokens, libraries) {
		plans = append(plans, candidate{
			source:      "context7_setup",
			libraryName: "context7",
			query:       compactQuery(promptText),
			priority:    95,
		})
	}
	for _, library := range libraries {
		if library == "context7" {
			continue
		}
		plans = append(plans, candidate{
			source:      "context7",
			libraryName: library,
			query:       buildQuery(library, promptText),
			priority:    90,
		})
		if opensrcLibraries[library] && hasAnyToken(tokens, sourceIntentTerms) {
			plans = append(plans, candidate{
				source:      "opensrc",
				libraryName: library,
				query:       buildQuery(library, promptText),
				priority:    70,
			})
		}
	}

	if len(plans) == 0 && docsIntent {
		plans = append(plans, candidate{
			source:   "exa",
			query:    compactQuery(promptText),
			priority: 40,
		})
	}

	sort.SliceStable(plans, func(i, j int) bool {
		if plans[i].priority == plans[j].priority {
			return plans[i].source < plans[j].source
		}
		return plans[i].priority > plans[j].priority
	})
	return dedupePlans(plans)
}

func isContext7SetupPrompt(tokens, libraries []string) bool {
	if !hasAnyToken(tokens, setupIntentTerms) {
		return false
	}
	for _, library := range libraries {
		if library == "context7" {
			return true
		}
	}
	return false
}

func detectLibraries(tokens []string) []string {
	seen := make(map[string]bool)
	var libraries []string
	for _, token := range tokens {
		library, ok := stackAliases[token]
		if !ok || seen[library] {
			continue
		}
		seen[library] = true
		libraries = append(libraries, library)
	}
	sort.Strings(libraries)
	return libraries
}

func inferFailureLibrary(commandFamily, errorKind, normalizedMessage, payload string) string {
	commandLower := strings.ToLower(commandFamily)
	errorLower := strings.ToLower(errorKind)
	messageLower := strings.ToLower(normalizedMessage)
	payloadText := strings.ToLower(strings.Join(extractPayloadStrings(payload), " "))
	text := strings.Join([]string{
		commandFamily,
		errorKind,
		normalizedMessage,
		strings.Join(extractPayloadStrings(payload), " "),
	}, " ")

	switch {
	case strings.HasPrefix(commandLower, "cargo"), strings.HasPrefix(commandLower, "rustc"), strings.Contains(errorLower, "rust"):
		return "rust"
	case strings.HasPrefix(commandLower, "go "):
		return "go"
	case strings.HasPrefix(commandLower, "pytest"), strings.HasPrefix(commandLower, "python"):
		return "python"
	case strings.Contains(messageLower, "invalid hook call"), strings.Contains(messageLower, "rules of hooks"), strings.Contains(messageLower, "hook") && strings.Contains(payloadText, "react"):
		return "react"
	case strings.Contains(messageLower, "tailwind"), strings.Contains(payloadText, "tailwind"):
		return "tailwindcss"
	case strings.Contains(messageLower, "prisma"), strings.Contains(payloadText, "prisma"):
		return "prisma"
	case strings.Contains(messageLower, "next"), strings.Contains(payloadText, "next.js"), strings.Contains(payloadText, "next/router"):
		return "next.js"
	case strings.Contains(messageLower, "ts2304"), strings.Contains(messageLower, "typescript"), strings.Contains(payloadText, "tsc"):
		return "typescript"
	}

	if strings.HasPrefix(commandLower, "npm") || strings.HasPrefix(commandLower, "pnpm") || strings.HasPrefix(commandLower, "yarn") || strings.HasPrefix(commandLower, "bun") || strings.HasPrefix(commandLower, "node") {
		for _, candidate := range []string{"next.js", "react", "prisma", "tailwindcss", "typescript", "node.js"} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(candidate)) {
				return candidate
			}
		}
	}

	libraries := detectLibraries(tokenize(text))
	if len(libraries) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(errorKind), "rust") {
		return "rust"
	}
	return libraries[0]
}

func buildFailureQuery(library, commandFamily, errorKind, normalizedMessage string) string {
	parts := []string{library}
	if commandFamily != "" {
		parts = append(parts, commandFamily)
	}
	if errorKind != "" && !strings.Contains(strings.ToLower(normalizedMessage), strings.ToLower(errorKind)) {
		parts = append(parts, errorKind)
	}
	if normalizedMessage != "" {
		parts = append(parts, normalizedMessage)
	}
	parts = append(parts, "official docs explanation and fix in one short sentence")
	return compactQuery(strings.Join(parts, " "))
}

func buildQuery(library, promptText string) string {
	query := compactQuery(promptText)
	if library == "" {
		return query
	}
	if strings.Contains(strings.ToLower(query), strings.ToLower(library)) {
		return query
	}
	return library + " " + query
}

func compactQuery(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) > 180 {
		return strings.TrimSpace(text[:180])
	}
	return text
}

func extractPayloadStrings(payload string) []string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}

	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return []string{payload}
	}

	var out []string
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	walk(parsed)
	return out
}

func hasAnyToken(tokens []string, want map[string]bool) bool {
	for _, token := range tokens {
		if want[token] {
			return true
		}
	}
	return false
}

func tokenize(text string) []string {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '/'
	})
	var out []string
	seen := make(map[string]bool)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) < 2 || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func dedupePlans(plans []candidate) []candidate {
	seen := make(map[string]bool)
	var out []candidate
	for _, plan := range plans {
		key := plan.source + "|" + plan.libraryName + "|" + plan.query
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, plan)
	}
	return out
}

func dedupeKey(projectID, source, libraryName, query string) string {
	sum := sha256.Sum256([]byte(projectID + "|" + source + "|" + libraryName + "|" + strings.ToLower(strings.TrimSpace(query))))
	return fmt.Sprintf("%x", sum[:8])
}
