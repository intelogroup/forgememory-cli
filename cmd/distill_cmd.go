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

	events, err := database.UndistilledEvents(300)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No undistilled events.")
		return
	}

	fmt.Printf("Distilling %d events...\n", len(events))
	start := time.Now()
	count, err := d.DistillBatch(300)
	recordHealthResult(database, cfg, start, len(events), count, err)
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

func recordHealthResult(database *db.DB, cfg distill.Config, start time.Time, attemptedEvents int, principlesCount int, err error) {
	duration := time.Since(start)
	if err != nil {
		if !shouldSkipDistillForMissingProvider(cfg, err) {
			_ = database.RecordDistillationFailure(start, duration, attemptedEvents, err.Error(), time.Now().Add(cfg.DistillInterval))
		}
	} else {
		_ = database.RecordDistillationSuccess(start, duration, attemptedEvents, principlesCount, time.Now().Add(cfg.DistillInterval))
	}
}

func runDistillDrain(d *distill.Distiller, database *db.DB, cfg distill.Config) {
	totalPrinciples := 0
	stalledOnce := false
	stalled := false
	for {
		_, remaining, _ := database.EventCount()
		if remaining == 0 {
			break
		}
		start := time.Now()
		// Drain includes session_id='unknown' orphans so the backlog flushes
		// instead of silently stalling behind them forever (#34).
		count, err := d.DistillBatchIncludingUnknown(300)
		recordHealthResult(database, cfg, start, remaining, count, err)
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
				// No events consumed AND no principles — true no-op. We give the
				// drain ONE retry (some sessions legitimately start with <3 events
				// then grow), but if it stalls twice in a row we stop and surface
				// it as a distinct state instead of pretending "Drain complete".
				// This prevents the silent-success-while-frozen failure mode (#34).
				if stalledOnce {
					fmt.Fprintf(os.Stderr, "Drain stalled: %d events remain undistilled after two consecutive no-op batches.\n", newRemaining)
					fmt.Fprintf(os.Stderr, "Run `forge doctor` to diagnose. Recent events may lack enough 3+ related-action context for the LLM to extract a principle.\n")
					_ = database.RecordDistillationFailure(start, time.Since(start), remaining,
						"drain stalled: no events consumed across two consecutive no-op batches",
						time.Now().Add(cfg.DistillInterval))
					stalled = true
					break
				}
				stalledOnce = true
				continue
			}
			stalledOnce = false
			if newRemaining < 3 {
				break
			}
			continue
		}
		stalledOnce = false
		totalPrinciples += count
		_, newRemaining, _ := database.EventCount()
		fmt.Printf("Distilled %d principle(s). %d events remaining...\n", count, newRemaining)
	}
	_, finalRemaining, _ := database.EventCount()
	if stalled {
		// Exit non-zero so callers (e.g. `--wait` flow) can distinguish a real
		// stall from a successful drain. Mirrors the err != nil path above.
		fmt.Fprintf(os.Stderr, "Drain stalled. %d total principle(s) distilled. %d events remaining.\n",
			totalPrinciples, finalRemaining)
		os.Exit(1)
	}
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
