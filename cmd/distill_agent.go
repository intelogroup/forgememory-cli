package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/mcp"
)

const (
	chunkSize     = 50
	directCutoff  = 100 // sessions <= this many events use direct synthesis
)

// runDistillAgent is an internal command spawned by the daemon distill loop.
// It gathers context via Forge MCP tools, then runs context-aware synthesis.
// For large sessions (>100 events), it processes events in chunks and
// synthesizes a master summary from the chunk summaries.
func runDistillAgent(args []string) {
	fs := flag.NewFlagSet("distill-agent", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "Session ID")
	projectID := fs.String("project-id", "", "Project ID")
	checkpointKind := fs.String("checkpoint-kind", "daemon", "Checkpoint kind")
	checkpointKey := fs.String("checkpoint-key", "", "Stable checkpoint key")
	cutoffTS := fs.String("cutoff-ts", "", "Optional timestamp cutoff")
	if err := fs.Parse(args); err != nil {
		log.Printf("distill-agent: flag parse error: %v", err)
		os.Exit(0)
	}

	if *sessionID == "" {
		os.Exit(0)
	}

	database, err := db.Open("")
	if err != nil {
		log.Printf("distill-agent: failed to open DB: %v", err)
		os.Exit(0)
	}
	defer database.Close()

	if *checkpointKey != "" {
		exists, err := database.HasSessionCheckpoint(*checkpointKey)
		if err == nil && exists {
			os.Exit(0)
		}
	}

	// Load ALL events for the session (no hard limit — progressive chunking
	// handles large sessions below).
	events, err := database.SessionEventsUpTo(*sessionID, *cutoffTS, 0)
	if err != nil {
		log.Printf("distill-agent: failed to get session events: %v", err)
		os.Exit(0)
	}
	if len(events) < 3 {
		_ = database.MarkSessionDistilled(*sessionID)
		os.Exit(0)
	}

	proj := *projectID
	if proj == "" {
		proj = events[0].ProjectID
	}

	runAt := time.Now().UTC()
	start := time.Now()

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)

	// Stage 1: Gather context via MCP (optional — fall back to context-free if unavailable)
	contextBlock := gatherMCPContext(proj)
	if contextBlock != "" {
		d.SetSessionContext(contextBlock)
		log.Printf("distill-agent: gathered context for %s (session %s, %d events)", proj, *sessionID, len(events))
	} else {
		log.Printf("distill-agent: no MCP context available, proceeding without context")
	}

	// Stage 2: Context-aware synthesis
	var summary *db.SessionSummary
	var principlesCount int

	if len(events) <= directCutoff {
		// Normal path: synthesize all events in one pass.
		summary, err = d.SynthesizeCheckpoint(*sessionID, proj, *checkpointKind, *checkpointKey, events)
		if err != nil {
			log.Printf("distill-agent: synthesis failed: %v", err)
			_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
			os.Exit(1)
		}
		if summary == nil {
			_ = database.MarkSessionDistilled(*sessionID)
			os.Exit(0)
		}
		count, pErr := d.DistillCheckpointSummary(*summary, events)
		if pErr != nil {
			log.Printf("distill-agent: principle extraction failed: %v", pErr)
		}
		principlesCount = count
	} else {
		// Large session path: chunk events, synthesize each chunk, then
		// produce a master summary from the chunk summaries.
		summary, principlesCount, err = synthesizeLargeSession(d, database, events, *sessionID, proj, *checkpointKind, *checkpointKey)
		if err != nil {
			log.Printf("distill-agent: large session synthesis failed: %v", err)
			_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
			os.Exit(1)
		}
	}

	_ = database.MarkSessionDistilled(*sessionID)
	_ = database.RecordDistillationSuccess(runAt, time.Since(start), len(events), principlesCount, runAt)
	log.Printf("distill-agent: session %s → %d events (%d chunks), %d principle(s) (context: %t)",
		*sessionID, len(events), (len(events)+chunkSize-1)/chunkSize, principlesCount, contextBlock != "")
	os.Exit(0)
}

// synthesizeLargeSession handles sessions exceeding directCutoff events by
// chunking them into groups of chunkSize, synthesizing each chunk independently,
// then producing a master summary from the chunk summaries.
func synthesizeLargeSession(d *distill.Distiller, database *db.DB, events []db.Event, sessionID, projectID, checkpointKind, checkpointKey string) (*db.SessionSummary, int, error) {
	// Chunk events into groups of chunkSize
	var chunks [][]db.Event
	for i := 0; i < len(events); i += chunkSize {
		end := i + chunkSize
		if end > len(events) {
			end = len(events)
		}
		chunks = append(chunks, events[i:end])
	}

	// Synthesize each chunk
	var chunkSummaries []parsedChunkSummary
	for i, chunk := range chunks {
		prompt := buildChunkSummaryPrompt(chunk, i+1, len(chunks))
		response, err := d.CallLLM(prompt)
		if err != nil {
			return nil, 0, fmt.Errorf("chunk %d llm: %w", i, err)
		}
		cs, err := parseChunkSummaryResponse(response)
		if err != nil {
			return nil, 0, fmt.Errorf("chunk %d parse: %w", i, err)
		}
		cs.index = i
		cs.events = len(chunk)
		chunkSummaries = append(chunkSummaries, cs)
	}

	// Build master prompt from chunk summaries
	masterPrompt := buildMasterSummaryPrompt(chunkSummaries)
	response, err := d.CallLLM(masterPrompt)
	if err != nil {
		return nil, 0, fmt.Errorf("master synthesis llm: %w", err)
	}

	finalRaw, err := parseChunkSummaryResponse(response)
	if err != nil {
		return nil, 0, fmt.Errorf("master synthesis parse: %w", err)
	}

	summary := &db.SessionSummary{
		TS:             time.Now().UTC().Format(time.RFC3339),
		SessionID:      sessionID,
		ProjectID:      projectID,
		CheckpointKind: checkpointKind,
		CheckpointKey:  checkpointKey,
		Request:        finalRaw.request,
		Investigation:  finalRaw.investigation,
		Learnings:      finalRaw.learnings,
		NextSteps:      finalRaw.nextSteps,
		Keywords:       strings.Join(finalRaw.keywords, " "),
		Summary:        finalRaw.learnings,
	}
	if err := database.InsertSessionSummary(summary); err != nil {
		return nil, 0, fmt.Errorf("insert summary: %w", err)
	}

	// Extract principles from the master summary. Supply the chunk summaries
	// as context so the LLM has the full picture.
	principlesPrompt := buildChunkPrinciplesPrompt(finalRaw, chunkSummaries)
	resp, err := d.CallLLM(principlesPrompt)
	if err != nil {
		return summary, 0, fmt.Errorf("principles llm: %w", err)
	}

	// Use the existing principle parsing from Distiller
	principles, pErr := d.ParsePrinciples(resp, projectID)
	if pErr != nil {
		return summary, 0, fmt.Errorf("parse principles: %w", pErr)
	}

	count := 0
	for _, p := range principles {
		ok, err := database.InsertPrinciple(&p)
		if err != nil {
			return summary, count, fmt.Errorf("insert principle: %w", err)
		}
		if ok {
			count++
		}
	}

	return summary, count, nil
}



// buildChunkSummaryPrompt builds an LLM prompt for a single event chunk.
func buildChunkSummaryPrompt(events []db.Event, chunkNum, totalChunks int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("This is chunk %d of %d from a coding session. Analyze these events and produce a concise summary.\n\n", chunkNum, totalChunks))
	sb.WriteString("Return JSON with:\n")
	sb.WriteString("- request: What was the user trying to accomplish in this chunk? (1 sentence)\n")
	sb.WriteString("- investigation: What was explored or debugged? (1-2 sentences)\n")
	sb.WriteString("- learnings: Key findings or solutions discovered (1-2 sentences)\n")
	sb.WriteString("- next_steps: Recommended next actions (1 sentence, or empty string)\n")
	sb.WriteString("- keywords: Array of 3-5 key technical terms from this chunk\n\n")
	sb.WriteString("Events:\n")
	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, distill.ShortTS(e.TS), e.EventType, e.ToolName, distill.SummarizeEventPayload(e)))
	}
	sb.WriteString("\nReturn ONLY the JSON object, no other text.\n")
	return sb.String()
}

// buildMasterSummaryPrompt builds a prompt that synthesizes chunk summaries
// into a coherent session-level summary.
func buildMasterSummaryPrompt(chunks []parsedChunkSummary) string {
	var sb strings.Builder
	sb.WriteString("Below are summaries of sequential chunks from a single coding session. Synthesize them into a coherent session-level summary.\n\n")
	for _, cs := range chunks {
		sb.WriteString(fmt.Sprintf("--- Chunk %d (%d events) ---\n", cs.index+1, cs.events))
		sb.WriteString(fmt.Sprintf("Request: %s\n", cs.request))
		sb.WriteString(fmt.Sprintf("Investigation: %s\n", cs.investigation))
		sb.WriteString(fmt.Sprintf("Learnings: %s\n", cs.learnings))
		sb.WriteString(fmt.Sprintf("Next steps: %s\n", cs.nextSteps))
		if len(cs.keywords) > 0 {
			sb.WriteString(fmt.Sprintf("Keywords: %s\n", strings.Join(cs.keywords, ", ")))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Now produce the combined session summary. Return JSON with:\n")
	sb.WriteString("- request: What was the overall goal of the session? (1 sentence)\n")
	sb.WriteString("- investigation: What was explored overall? (1-2 sentences)\n")
	sb.WriteString("- learnings: Key overall findings (2-3 sentences)\n")
	sb.WriteString("- next_steps: Recommended next actions (1 sentence, or empty string)\n")
	sb.WriteString("- keywords: Array of 3-7 key technical terms for the full session\n")
	sb.WriteString("\nReturn ONLY the JSON object, no other text.\n")
	return sb.String()
}

// buildChunkPrinciplesPrompt builds a prompt for principle extraction from
// chunk summaries.
func buildChunkPrinciplesPrompt(final parsedChunkSummary, chunks []parsedChunkSummary) string {
	var sb strings.Builder
	sb.WriteString("You are a senior staff engineer writing personal reference notes for your future self. Extract only durable, project-specific engineering principles from this coding session.\n\n")
	sb.WriteString("Session summary:\n")
	sb.WriteString(fmt.Sprintf("Goal: %s\n", final.request))
	sb.WriteString(fmt.Sprintf("What was done: %s\n", final.investigation))
	sb.WriteString(fmt.Sprintf("Key learnings: %s\n", final.learnings))
	sb.WriteString(fmt.Sprintf("Keywords: %s\n\n", strings.Join(final.keywords, ", ")))

	sb.WriteString("Chunk breakdown:\n")
	for _, cs := range chunks {
		sb.WriteString(fmt.Sprintf("  Chunk %d: %s | %s\n", cs.index+1, cs.request, cs.learnings))
	}
	sb.WriteString("\nReturn a JSON array. Each element must have:\n")
	sb.WriteString("- type: architecture/bugfix/pattern/preference\n")
	sb.WriteString("- title: short description (max 80 chars)\n")
	sb.WriteString("- narrative: 2-4 sentences explaining the technique and why it matters\n")
	sb.WriteString("- impact_score: 0.0-1.0\n")
	sb.WriteString("- outcome: \"success\"/\"failure\"/\"unknown\"\n")
	sb.WriteString("- impl_hint: exact function, flag, or pattern used (max 120 chars, or empty)\n")
	sb.WriteString("- concepts: array from [security, pattern, gotcha, performance, trade-off, how-it-works]\n")
	sb.WriteString("- files_modified: array of file paths (empty if none)\n\n")
	sb.WriteString("Only emit a principle for concrete, reusable techniques. Return [] if the session was routine.\n")
	sb.WriteString("Return ONLY the JSON array, no other text.\n")
	return sb.String()
}

type parsedChunkSummary struct {
	index        int      // position in chunk sequence (not from JSON)
	events       int      // number of events in this chunk (not from JSON)
	request      string   `json:"request"`
	investigation string   `json:"investigation"`
	learnings    string   `json:"learnings"`
	nextSteps    string   `json:"next_steps"`
	keywords     []string `json:"keywords"`
}

func parseChunkSummaryResponse(response string) (parsedChunkSummary, error) {
	response = strings.TrimSpace(response)
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start >= 0 && end > start {
		response = response[start : end+1]
	}
	var raw parsedChunkSummary
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return raw, fmt.Errorf("parse JSON: %w", err)
	}
	return raw, nil
}



// gatherMCPContext connects to Forge MCP and gathers existing knowledge about a project.
// Returns a formatted context block, or empty string if MCP is unavailable.
func gatherMCPContext(projectID string) string {
	client, err := mcp.NewClient()
	if err != nil {
		return ""
	}
	defer client.Close()

	args := map[string]any{"project_id": projectID}

	var sb strings.Builder
	sb.WriteString("[EXISTING KNOWLEDGE — what Forge already knows about this project]\n\n")

	// Gather principles
	if text, err := client.CallTool("get_principles", mergeArgs(args, map[string]any{"limit": float64(10)})); err == nil && text != "" {
		sb.WriteString("Existing principles:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	// Gather session summaries
	if text, err := client.CallTool("get_session_summaries", mergeArgs(args, map[string]any{"limit": float64(5)})); err == nil && text != "" {
		sb.WriteString("Past sessions:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	// Gather cross-session patterns
	if text, err := client.CallTool("get_cross_session_patterns", mergeArgs(args, map[string]any{"limit": float64(10)})); err == nil && text != "" {
		sb.WriteString("Cross-session patterns:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return ""
	}

	result += "\n\n[END EXISTING KNOWLEDGE]\n"
	result += "\nThe block above shows what we already know about this project. Use it to avoid duplicating existing knowledge. The new session events follow below.\n"
	return result
}

// formatMCPText indents and cleans up text from MCP tool responses.
func formatMCPText(text, indent string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip headers (##, ###) and separator lines (---, ___)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") && len(trimmed) > 1 && trimmed[1] == '-' || strings.HasPrefix(trimmed, "*") {
			continue
		}
		out = append(out, indent+trimmed)
	}
	return strings.Join(out, "\n") + "\n"
}

func mergeArgs(a, b map[string]any) map[string]any {
	result := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		result[k] = v
	}
	for k, v := range b {
		result[k] = v
	}
	return result
}
