package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/recall"
)

type recallArgs struct {
	query   string
	project string
	all     bool
	json    bool
	limit   int
}

func parseRecallArgs(args []string) (recallArgs, error) {
	parsed := recallArgs{limit: recall.DefaultLimit}
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			parsed.all = true
		case "--json":
			parsed.json = true
		case "--project", "--limit":
			if i+1 >= len(args) {
				return parsed, fmt.Errorf("%s needs a value", args[i])
			}
			i++
			if args[i-1] == "--project" {
				parsed.project = args[i]
				continue
			}
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit <= 0 {
				return parsed, fmt.Errorf("--limit must be a positive number, got %q", args[i])
			}
			parsed.limit = limit
		default:
			words = append(words, args[i])
		}
	}
	parsed.query = strings.TrimSpace(strings.Join(words, " "))
	if parsed.query == "" {
		return parsed, fmt.Errorf("missing query")
	}
	return parsed, nil
}

func runRecall(args []string) {
	parsed, err := parseRecallArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\nUsage: forge recall <query> [--all] [--project <id>] [--limit <n>] [--json]\n", err)
		os.Exit(1)
	}
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	scope := db.RecallScope{All: parsed.all}
	switch {
	case parsed.all:
	case parsed.project != "":
		scope.ProjectID = parsed.project
	default:
		scope.ProjectID, scope.ProjectKey = detectProjectID(), currentProjectKey()
	}

	resp, err := recall.SearchFromEnv(context.Background(), database, parsed.query, scope, parsed.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if parsed.json {
		b, _ := json.MarshalIndent(map[string]any{
			"schema_version": "1",
			"query":          parsed.query,
			"mode":           resp.Mode,
			"reason":         resp.Reason,
			"results":        resp.Results,
		}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Print(recall.Render(resp))
}
