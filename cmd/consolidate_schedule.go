package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// consolidationTimeout bounds one scheduled consolidation run so a hung model
// call cannot keep the maintenance slot forever.
const consolidationTimeout = 30 * time.Minute

// consolidationArgs is the CLI invocation used by the scheduler. It is a
// variable only so tests can assert the contract without spawning a process.
var consolidationArgs = []string{"consolidate", "--apply"}

// consolidationCommand runs one apply pass. Tests replace it.
var consolidationCommand = func(args []string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate forge binary: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), consolidationTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

// consolidationInterval reads FORGE_CONSOLIDATE_INTERVAL. Unset, invalid, or
// non-positive values disable scheduled consolidation: it spends inference
// calls, so maintenance is opt-in.
func consolidationInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("FORGE_CONSOLIDATE_INTERVAL"))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("consolidate: invalid FORGE_CONSOLIDATE_INTERVAL %q: %v", raw, err)
		return 0
	}
	if d <= 0 {
		return 0
	}
	return d
}

// consolidationLoop runs the consolidation CLI on an interval until stop.
func consolidationLoop(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		start := time.Now()
		out, err := consolidationCommand(consolidationArgs)
		if err != nil {
			log.Printf("consolidate: scheduled run failed after %s: %v: %s",
				time.Since(start).Round(time.Second), err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("consolidate: scheduled run finished in %s", time.Since(start).Round(time.Second))
		}
		timer.Reset(interval)
	}
}
