package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/streams"
)

// runStreams groups a project's sessions into multi-day work streams —
// runs of sessions with no gap larger than --gap between them.
func runStreams(args []string) {
	fs := flag.NewFlagSet("streams", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	gap := fs.Duration("gap", streams.DefaultGap, "max idle time between sessions to still count as the same stream")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	projectID := detectProjectIDForPath(repoPath)

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(ranges) == 0 {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}

	ws, err := streams.Cluster(ranges, *gap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error clustering: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s: %d work streams from %d sessions (gap threshold %s)\n\n", projectID, len(ws), len(ranges), gap.String())
	for i, s := range ws {
		dur := s.End.Sub(s.Start)
		fmt.Printf("Stream %d: %s -> %s (%s, %d sessions, %d events)\n",
			i+1, s.Start.Format("2006-01-02 15:04"), s.End.Format("2006-01-02 15:04"),
			roundDuration(dur), len(s.SessionIDs), s.Events)
	}
}

func roundDuration(d time.Duration) string {
	return d.Round(time.Minute).String()
}
