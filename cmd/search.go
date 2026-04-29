package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
)

func runSearch(args []string) {
	jsonOutput := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
			continue
		}
		filtered = append(filtered, a)
	}

	if len(filtered) < 1 {
		fmt.Println("Usage: forge search <query>")
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	query := filtered[0]
	events, err := database.SearchEvents(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		if jsonOutput {
			payload := map[string]any{
				"schema_version": "1",
				"query":          query,
				"results":        []db.Event{},
			}
			b, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Println("No results.")
		return
	}

	if jsonOutput {
		payload := map[string]any{
			"schema_version": "1",
			"query":          query,
			"results":        events,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(b))
		return
	}

	for _, e := range events {
		fmt.Printf("[%s] %s (%s)\n", e.TS[:10], e.EventType, e.SourceTool)
		fmt.Printf("  %s\n", truncate(e.Payload, 200))
		fmt.Println()
	}
}
