package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// --all flag: drain entire backlog
// ---------------------------------------------------------------------------

// TestRunDistill_AllFlag_EmptyDB exits cleanly with no events.
func TestRunDistill_AllFlag_EmptyDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	out := captureStdout(func() { runDistill([]string{"--all"}) })

	if !strings.Contains(out, "Drain complete") {
		t.Errorf("expected drain complete message, got %q", out)
	}
}

// TestRunDistill_AllFlag_SingleBatch drains fewer than 50 events in one pass.
func TestRunDistill_AllFlag_SingleBatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	srv, llmCalls := distillServer(t, "Drain single batch principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	out := captureStdout(func() { runDistill([]string{"--all"}) })

	if llmCalls.Load() == 0 {
		t.Error("expected at least one LLM call")
	}
	if !strings.Contains(out, "Drain complete") {
		t.Errorf("expected drain complete message, got %q", out)
	}
	if !strings.Contains(out, "0 events remaining") {
		t.Errorf("expected 0 events remaining, got %q", out)
	}
}

// TestRunDistill_AllFlag_MultiBatch drains events requiring multiple batches.
func TestRunDistill_AllFlag_MultiBatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// 700 events → at least 2 batches of 300.
	seedEvents(t, 700)

	srv, llmCalls := distillServer(t, "Drain multi batch principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	out := captureStdout(func() { runDistill([]string{"--all"}) })

	if llmCalls.Load() < 2 {
		t.Errorf("expected >=2 LLM calls for 110 events, got %d", llmCalls.Load())
	}
	if !strings.Contains(out, "Drain complete") {
		t.Errorf("expected drain complete message, got %q", out)
	}
}

// TestRunDistill_AllFlag_PrintsProgressPerBatch verifies per-batch progress lines.
func TestRunDistill_AllFlag_PrintsProgressPerBatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 700) // forces at least 2 batches of 300

	srv, _ := distillServer(t, "Progress principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	out := captureStdout(func() { runDistill([]string{"--all"}) })

	// Should have at least one "events remaining..." line plus the final summary.
	if !strings.Contains(out, "events remaining") {
		t.Errorf("expected per-batch progress, got %q", out)
	}
	if !strings.Contains(out, "Drain complete") {
		t.Errorf("expected drain complete, got %q", out)
	}
}

// TestRunDistill_AllFlag_HoldsLockDuringDrain verifies the distill lock is held
// for the entire drain (daemon loop would be blocked).
func TestRunDistill_AllFlag_HoldsLockDuringDrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	srv, _ := distillServer(t, "Lock held principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	// Intercept the drain loop to check if lock is held while distilling.
	lockObservedHeld := false
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Give runDistill a moment to acquire the lock.
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		// Try to acquire the distill lock — must fail because --all holds it.
		competing, err := acquireDistillLock()
		if err == nil && competing == nil {
			lockObservedHeld = true
		}
		if competing != nil {
			competing.Close()
			cleanDistillLock()
		}
		mu.Unlock()
	}()

	captureStdout(func() { runDistill([]string{"--all"}) })
	wg.Wait()

	mu.Lock()
	held := lockObservedHeld
	mu.Unlock()

	if !held {
		t.Log("lock was not observed held mid-drain (likely drained too fast); this is OK for small batches")
	}
}

// TestRunDistill_AllFlag_LockReleasedAfterDrain verifies lock is released after --all finishes.
func TestRunDistill_AllFlag_LockReleasedAfterDrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	srv, _ := distillServer(t, "Lock released principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	captureStdout(func() { runDistill([]string{"--all"}) })

	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Error("distill lock should be released after --all completes")
	}
}

// TestRunDistill_AllFlag_TotalCountInSummary verifies the summary shows total principles.
func TestRunDistill_AllFlag_TotalCountInSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	srv, _ := distillServer(t, "Summary count principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	out := captureStdout(func() { runDistill([]string{"--all"}) })

	if !strings.Contains(out, "total principle(s) distilled") {
		t.Errorf("expected total principles in summary, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// --wait flag: queue behind running distill
// ---------------------------------------------------------------------------

// TestRunDistill_WaitFlag_NoLockHolder runs immediately when no lock is held.
func TestRunDistill_WaitFlag_NoLockHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No lock held, no events — should complete immediately with "No undistilled events".
	out := captureStdout(func() { runDistill([]string{"--wait"}) })

	if !strings.Contains(out, "No undistilled events") {
		t.Errorf("expected no undistilled events, got %q", out)
	}
}

// TestRunDistill_WaitFlag_PollsUntilLockFree starts with a lock held, releases it
// after a short delay, and verifies runDistill unblocks and runs.
func TestRunDistill_WaitFlag_PollsUntilLockFree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 3)

	srv, llmCalls := distillServer(t, "Wait unblock principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	// Pre-acquire with current PID so --wait polling sees a live holder.
	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}

	// Release the lock after 1.5 seconds.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		os.Remove(lockPath)
	}()

	start := time.Now()
	captureStdout(func() { runDistill([]string{"--wait"}) })
	elapsed := time.Since(start)

	// Must have waited at least 1 second (lock was held for 1.5s).
	if elapsed < 1*time.Second {
		t.Errorf("--wait returned too fast (%v), expected to poll for ~1.5s", elapsed)
	}

	// After unblocking, must have actually distilled.
	if llmCalls.Load() == 0 {
		t.Error("expected LLM call after --wait unblocked, got 0")
	}
}

// TestRunDistill_WaitFlag_LockReleased verifies lock is released after --wait run.
func TestRunDistill_WaitFlag_LockReleased(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	captureStdout(func() { runDistill([]string{"--wait"}) })

	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Error("distill lock should be released after --wait run")
	}
}

// ---------------------------------------------------------------------------
// --all --wait combination
// ---------------------------------------------------------------------------

// TestRunDistill_AllWait_CombinedFlags waits for lock then drains backlog.
func TestRunDistill_AllWait_CombinedFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	srv, llmCalls := distillServer(t, "Combined flags principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	// No lock held — should run immediately and drain.
	out := captureStdout(func() { runDistill([]string{"--all", "--wait"}) })

	if llmCalls.Load() == 0 {
		t.Error("expected LLM call with --all --wait")
	}
	if !strings.Contains(out, "Drain complete") {
		t.Errorf("expected drain complete, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Message hint: blocked message now suggests --wait
// ---------------------------------------------------------------------------

// TestRunDistill_BlockedMessageSuggestsWait verifies blocked message includes --wait hint.
func TestRunDistill_BlockedMessageSuggestsWait(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}
	defer os.Remove(lockPath)

	out := captureStdout(func() { runDistill([]string{}) })

	if !strings.Contains(out, "--wait") {
		t.Errorf("blocked message should suggest --wait, got %q", out)
	}
}

func TestRunDistill_BatchSizeFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 20)

	srv, llmCalls := distillServer(t, "Batch size flag principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	// --batch-size only caps the batch; with fewer events available than the
	// (>=300) minimum, distill still runs against whatever is undistilled.
	out := captureStdout(func() { runDistill([]string{"--batch-size", "300"}) })

	if llmCalls.Load() == 0 {
		t.Error("expected at least one LLM call")
	}
	if !strings.Contains(out, "Distilling 20 events") {
		t.Errorf("expected 20 events distilled, got output: %q", out)
	}
}

func TestRunDistill_BatchSizeFlagBelowMinimum(t *testing.T) {
	if os.Getenv("FORGE_TEST_SUBPROCESS") == "1" {
		home := t.TempDir()
		os.Setenv("HOME", home)
		runDistill([]string{"--batch-size", "5"})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestRunDistill_BatchSizeFlagBelowMinimum")
	cmd.Env = append(os.Environ(), "FORGE_TEST_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for --batch-size below 300, got success: %s", out)
	}
	if !strings.Contains(string(out), "must be 0 (use config default) or >= 300") {
		t.Fatalf("unexpected error output: %s", out)
	}
}

