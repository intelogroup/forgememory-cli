package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// runSave stores a memory directly, bypassing the daemon.
// If --principle is given, it inserts straight into the principles table (no LLM).
// Otherwise it inserts as an event and triggers immediate distillation.
func runSave(args []string) {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	typeFlag := fs.String("type", "note", "Memory type: success|failure|plan|note")
	content := fs.String("content", "", "What to remember")
	principle := fs.String("principle", "", "Principle text (skips LLM distillation)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *content == "" {
		fmt.Fprintln(os.Stderr, "Error: --content is required")
		fmt.Fprintln(os.Stderr, "Usage: forge save --type [success|failure|plan|note] --content TEXT [--principle TEXT]")
		os.Exit(1)
	}

	validTypes := map[string]bool{"success": true, "failure": true, "plan": true, "note": true}
	if !validTypes[*typeFlag] {
		fmt.Fprintf(os.Stderr, "Error: --type must be one of: success, failure, plan, note\n")
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	projectID := detectProjectID()

	if *principle != "" {
		// Direct insert into principles table — no LLM needed.
		p := &db.Principle{
			Type:        *typeFlag,
			Title:       truncate(*content, 80),
			Narrative:   *principle,
			ImpactScore: 0.7,
			ProjectID:   projectID,
		}
		if _, err := database.InsertPrinciple(p); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving principle: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved principle: %s\n", p.Title)
		return
	}

	// Insert as event, then synthesize/distill a manual checkpoint immediately.
	event := &db.Event{
		SessionID:  "manual-save",
		ProjectID:  projectID,
		SourceTool: "manual",
		EventType:  "ManualSave",
		Payload:    fmt.Sprintf(`{"type":%q,"content":%q}`, *typeFlag, *content),
	}
	if err := database.InsertEvent(event); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving event: %v\n", err)
		os.Exit(1)
	}

	d := distill.New(database, distill.LoadConfig())
	summary, err := d.SynthesizeCheckpoint("manual-save", projectID, "manual-save", checkpointKey("manual-save", "manual-save", event.ID), []db.Event{*event})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Saved but checkpoint synthesis failed: %v\n", err)
		fmt.Println("Memory saved.")
		return
	}
	count, err := d.DistillCheckpointSummary(*summary, []db.Event{*event})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Saved but principle distillation failed: %v\n", err)
		fmt.Println("Memory saved.")
		return
	}
	if count > 0 {
		fmt.Printf("Memory saved and distilled into %d principle(s).\n", count)
	} else {
		fmt.Println("Memory saved.")
	}
}
