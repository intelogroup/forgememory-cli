package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/forge/forge/internal/artifacts"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/sanitize"
)

func parsePurgeArgs(args []string) (text string, apply bool) {
	var words []string
	for _, arg := range args {
		if arg == "--apply" {
			apply = true
			continue
		}
		words = append(words, arg)
	}
	return strings.Join(words, " "), apply
}

func purgeFail(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

func artifactStore(database *db.DB) artifacts.Store {
	return artifacts.Store{DB: database, Root: filepath.Join(filepath.Dir(database.Path), "artifacts")}
}

func printPurge(counts []db.PurgeCount, files int, apply bool) {
	if !apply {
		fmt.Println("Dry run: nothing changed. Run again with --apply.")
	}
	for _, c := range counts {
		fmt.Printf("  %s: %d\n", c.Target, c.Rows)
	}
	fmt.Printf("  artifact files: %d\n", files)
}

func runRedact(args []string) {
	extra, apply := parsePurgeArgs(args)
	if extra != "" {
		purgeFail(fmt.Errorf("usage: forge redact [--apply]"))
	}
	if addr := readAddr(); apply && addr != "" && isDaemonAlive(addr) {
		purgeFail(fmt.Errorf("stop the daemon first: compacting needs the database to itself"))
	}
	database, err := db.Open("")
	if err != nil {
		purgeFail(err)
	}
	defer database.Close()
	counts, err := database.RedactSecrets(apply)
	if err != nil {
		purgeFail(err)
	}
	files, err := artifactStore(database).Rewrite(sanitize.ScrubSecrets, apply)
	if err != nil {
		purgeFail(err)
	}
	if apply {
		if err := database.CompactAfterPurge(); err != nil {
			purgeFail(err)
		}
	}
	printPurge(counts, files, apply)
}

func runForget(args []string) {
	text, apply := parsePurgeArgs(args)
	pattern, err := db.ForgetPattern(text)
	if err != nil {
		purgeFail(fmt.Errorf("%w; usage: forge forget <text> [--apply]", err))
	}
	database, err := db.Open("")
	if err != nil {
		purgeFail(err)
	}
	defer database.Close()
	counts, unreadable, err := database.Forget(pattern, apply)
	if err != nil {
		purgeFail(err)
	}
	files, err := artifactStore(database).Rewrite(func(s string) string {
		return pattern.ReplaceAllLiteralString(s, db.ForgottenMarker)
	}, apply)
	if err != nil {
		purgeFail(err)
	}
	printPurge(counts, files, apply)
	if unreadable > 0 {
		fmt.Printf("  not searched: %d encrypted principle narratives that cannot be decrypted\n", unreadable)
	}
}
