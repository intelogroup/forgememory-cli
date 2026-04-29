package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

func runDistill(args []string) {
	fs := flag.NewFlagSet("distill", flag.ContinueOnError)
	allFlag := fs.Bool("all", false, "Drain entire undistilled backlog in 50-event batches")
	waitFlag := fs.Bool("wait", false, "Wait for lock if another distill is running, then run")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *waitFlag {
		for {
			pid := readLockPID(distillLockPath())
			if pid <= 0 || !isProcessAlive(pid) {
				break
			}
			fmt.Printf("\rWaiting for distillation lock (held by pid %d)...", pid)
			time.Sleep(1 * time.Second)
		}
		fmt.Println()
	}

	// Prevent concurrent distillation across terminals or daemon + CLI.
	lock, err := acquireDistillLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error acquiring distill lock: %v\n", err)
		os.Exit(1)
	}
	if lock == nil {
		pid := readLockPID(distillLockPath())
		if pid > 0 {
			fmt.Printf("Distillation already in progress (pid %d). Use --wait to queue.\n", pid)
		} else {
			fmt.Println("Distillation already in progress. Use --wait to queue.")
		}
		return
	}
	defer cleanDistillLock()
	defer lock.Close()

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)

	if *allFlag {
		runDistillDrain(d, database, cfg)
		return
	}

	events, err := database.UndistilledEvents(50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No undistilled events.")
		return
	}

	fmt.Printf("Distilling %d events...\n", len(events))
	count, err := d.DistillBatch(300)
	if err != nil {
		if shouldSkipDistillForMissingProvider(cfg, err) {
			fmt.Printf("Skipping distillation: %s\n", distill.UserMessage(err))
			fmt.Println("Events remain queued until you configure a provider or start Ollama.")
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", distill.UserMessage(err))
		fmt.Println("Suggestions:")
		for i, hint := range distill.DiagnosticHints(cfg, err) {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, hint)
		}
		os.Exit(1)
	}
	if count == 0 {
		fmt.Println("Not enough undistilled events to extract principles yet (need 3+ related events).")
		return
	}
	fmt.Printf("Distilled %d principle(s).\n", count)
}

func runDistillDrain(d *distill.Distiller, database *db.DB, cfg distill.Config) {
	totalPrinciples := 0
	for {
		_, remaining, _ := database.EventCount()
		if remaining == 0 {
			break
		}
		count, err := d.DistillBatch(300)
		if err != nil {
			if shouldSkipDistillForMissingProvider(cfg, err) {
				fmt.Printf("Skipping distillation: %s\n", distill.UserMessage(err))
				fmt.Println("Events remain queued until you configure a provider or start Ollama.")
				return
			}
			fmt.Fprintf(os.Stderr, "Drain error: %s\n", distill.UserMessage(err))
			os.Exit(1)
		}
		if count == 0 {
			// DistillBatch: 0 principles — events may still be undistilled (LLM
			// returned empty) or may have been auto-consumed (< 3 boundary).
			_, newRemaining, _ := database.EventCount()
			if newRemaining == remaining {
				// No events were consumed — LLM found nothing actionable.
				// Stop draining to avoid re-processing the same batch forever.
				break
			}
			if newRemaining < 3 {
				break
			}
			continue
		}
		totalPrinciples += count
		_, newRemaining, _ := database.EventCount()
		fmt.Printf("Distilled %d principle(s). %d events remaining...\n", count, newRemaining)
	}
	_, finalRemaining, _ := database.EventCount()
	fmt.Printf("Drain complete. %d total principle(s) distilled. %d events remaining.\n",
		totalPrinciples, finalRemaining)
}

func shouldSkipDistillForMissingProvider(cfg distill.Config, err error) bool {
	if hasExplicitInferenceProviderConfig() {
		return false
	}
	if cfg.Provider != distill.ProviderOllama {
		if cfg.Provider != distill.ProviderForgememo {
			return false
		}
	}
	errText := strings.ToLower(err.Error())
	return errors.Is(err, distill.ErrNoProvider) ||
		errors.Is(err, distill.ErrProviderUnreachable) ||
		(cfg.Provider == distill.ProviderForgememo && cfg.APIKey == "" && errors.Is(err, distill.ErrProviderInvalid)) ||
		strings.Contains(errText, "connection refused") ||
		strings.Contains(errText, "actively refused")
}

func hasExplicitInferenceProviderConfig() bool {
	if os.Getenv("FORGE_PROVIDER") != "" || os.Getenv("FORGE_API_KEY") != "" {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.Provider != "" || cfg.APIKey != ""
}
