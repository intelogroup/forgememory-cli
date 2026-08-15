package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// distillServer returns a test HTTP server that returns a valid Ollama distill
// response and counts how many times the LLM was actually called.
func distillServer(t *testing.T, title string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]string{
			"response": fmt.Sprintf(`[{"type":"pattern","title":%q,"narrative":"test","impact_score":0.7}]`, title),
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// captureStdoutString captures os.Stdout output from f (single-goroutine only).
func captureStdoutString(f func()) string {
	return captureStdout(f)
}

// ---------------------------------------------------------------------------
// Lock file basics
// ---------------------------------------------------------------------------

func TestDistillLock_AcquireAndRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock to be acquired (no competing holder)")
	}
	lock.Close()
	cleanDistillLock()

	// Lock file must be gone after release.
	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Error("distill lock file should not exist after cleanDistillLock")
	}
}

func TestDistillLock_SecondAcquireReturnNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock1, err := acquireDistillLock()
	if err != nil || lock1 == nil {
		t.Fatalf("first acquireDistillLock failed: err=%v lock=%v", err, lock1)
	}
	defer lock1.Close()
	defer cleanDistillLock()

	// A second acquire while the first is held must return (nil, nil).
	lock2, err := acquireDistillLock()
	if err != nil {
		t.Errorf("second acquire should not return error, got: %v", err)
	}
	if lock2 != nil {
		lock2.Close()
		t.Error("second acquire should return nil lock when already held")
	}
}

func TestDistillLock_StaleLockDeadPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// PID 99999999 is virtually certain to be dead.
	if err := os.WriteFile(lockPath, []byte("99999999"), 0o600); err != nil {
		t.Fatalf("WriteFile stale lock: %v", err)
	}

	// Stale lock must be auto-cleaned and acquisition must succeed.
	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock with stale lock: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock acquired after stale cleanup, got nil")
	}
	lock.Close()
	cleanDistillLock()
}

func TestDistillLock_GarbagePIDInLockFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Non-numeric content → treated as stale (pid=0 → dead).
	if err := os.WriteFile(lockPath, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatalf("WriteFile garbage lock: %v", err)
	}

	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock with garbage lock: %v", err)
	}
	if lock == nil {
		t.Fatal("expected garbage lock to be treated as stale and cleaned")
	}
	lock.Close()
	cleanDistillLock()
}

func TestDistillLock_PIDInLockFileMatchesCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Simulate another forge process (use current PID — it IS alive).
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile live pid lock: %v", err)
	}

	// Must NOT acquire — the "holder" is alive.
	lock, err := acquireDistillLock()
	if err != nil {
		t.Errorf("should return nil,nil not error when live lock held, got: %v", err)
	}
	if lock != nil {
		lock.Close()
		cleanDistillLock()
		t.Error("should not acquire lock when live pid holds it")
	}

	// Clean up manually.
	os.Remove(lockPath)
}

// ---------------------------------------------------------------------------
// runDistill integration: lock is acquired + released around the whole call
// ---------------------------------------------------------------------------

func TestRunDistill_LockReleasedAfterNoEvents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	captureStdoutString(func() { runDistill([]string{}) })

	// Lock must be released even when there's nothing to distill.
	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Error("distill lock file should not exist after runDistill completes with no events")
	}
}

func TestRunDistill_LockReleasedAfterSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 3)

	srv, _ := distillServer(t, "Lock release test principle")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	captureStdoutString(func() { runDistill([]string{}) })

	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Error("distill lock file should not exist after successful runDistill")
	}
}

func TestRunDistill_BlockedByLiveHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 3)

	// Pre-acquire the lock using current PID (simulates another terminal running distill).
	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}
	defer os.Remove(lockPath)

	srv, calls := distillServer(t, "Should not appear")
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	_ = srv

	out := captureStdoutString(func() { runDistill([]string{}) })

	if calls.Load() != 0 {
		t.Errorf("LLM called %d times despite lock being held; want 0", calls.Load())
	}
	if !strings.Contains(out, "already in progress") {
		t.Errorf("expected 'already in progress' message, got: %q", out)
	}
}

func TestRunDistill_BlockedMessageIncludesPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	holderPID := os.Getpid()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", holderPID)), 0o600); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}
	defer os.Remove(lockPath)

	out := captureStdoutString(func() { runDistill([]string{}) })

	if !strings.Contains(out, fmt.Sprintf("%d", holderPID)) {
		t.Errorf("expected holder PID %d in blocked message, got: %q", holderPID, out)
	}
}

// ---------------------------------------------------------------------------
// Race condition: concurrent runDistill calls
// ---------------------------------------------------------------------------

// TestDistillLock_ConcurrentAcquire fires N goroutines all trying to acquire
// the distill lock simultaneously. Exactly one must succeed; the rest must get
// nil (blocked). This is the core race-condition test — run with -race.
func TestDistillLock_ConcurrentAcquire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const goroutines = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		acquired []*os.File
		blocked  int
	)

	// All goroutines start together.
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // wait for all to be ready
			lock, err := acquireDistillLock()
			if err != nil {
				return // unexpected — count as nothing
			}
			mu.Lock()
			defer mu.Unlock()
			if lock != nil {
				acquired = append(acquired, lock)
			} else {
				blocked++
			}
		}()
	}

	close(start) // release all goroutines simultaneously
	wg.Wait()

	// Release all acquired locks.
	for _, l := range acquired {
		l.Close()
	}
	cleanDistillLock()

	if len(acquired) != 1 {
		t.Errorf("exactly 1 goroutine should acquire the lock, got %d", len(acquired))
	}
	if blocked != goroutines-1 {
		t.Errorf("%d goroutines should be blocked, got %d", goroutines-1, blocked)
	}
}

// TestDistillLock_NoDuplicateDistillation is the end-to-end race test:
// two goroutines call runDistill at the same time; the LLM must be called
// exactly once (not twice), and events must end up distilled exactly once.
func TestDistillLock_NoDuplicateDistillation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 5)

	// Use a slow server so goroutine 1 is still holding the lock when goroutine 2
	// calls acquireDistillLock. Without the delay, goroutine 1 can finish entirely
	// before goroutine 2 starts, causing a legitimate second LLM call.
	var slowLLMCalls atomic.Int32
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowLLMCalls.Add(1)
		time.Sleep(300 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{
			"response": `[{"type":"pattern","title":"Race condition principle","narrative":"test","impact_score":0.7}]`,
		})
	}))
	t.Cleanup(slowSrv.Close)
	llmCalls := &slowLLMCalls
	srv := slowSrv
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	// Redirect stdout to /dev/null for the duration — we only care about DB state.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	old := os.Stdout
	os.Stdout = devNull
	defer func() {
		os.Stdout = old
		devNull.Close()
	}()

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runDistill([]string{})
		}()
	}

	close(start)
	wg.Wait()

	// Give any async DB writes a moment.
	time.Sleep(50 * time.Millisecond)

	if got := llmCalls.Load(); got != 1 {
		t.Errorf("LLM called %d times; want exactly 1 (race guard failed)", got)
	}

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	_, undistilled, _ := database.EventCount()
	if undistilled != 0 {
		t.Errorf("undistilled = %d after concurrent distill; want 0", undistilled)
	}
	count, _ := database.PrincipleCount()
	if count != 1 {
		t.Errorf("principles = %d; want 1 (no duplicates from race)", count)
	}
}

// ---------------------------------------------------------------------------
// distillLoop: skips tick when manual forge distill holds the lock
// ---------------------------------------------------------------------------

// TestDistillLoop_SkipsWhenLockHeld verifies the guard inside distillLoop:
// when the distill lock is already held, acquireDistillLock returns nil
// and the daemon skips the LLM call for that tick.
func TestDistillLoop_SkipsWhenLockHeld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// First goroutine acquires the lock (simulates `forge distill` in another terminal).
	manualLock, err := acquireDistillLock()
	if err != nil || manualLock == nil {
		t.Fatalf("pre-acquire distill lock: err=%v lock=%v", err, manualLock)
	}
	defer manualLock.Close()
	defer cleanDistillLock()

	// Now simulate a daemon tick trying to acquire the same lock.
	daemonLock, err := acquireDistillLock()
	if err != nil {
		t.Errorf("daemon tick should get nil,nil not error: %v", err)
	}
	if daemonLock != nil {
		daemonLock.Close()
		t.Error("daemon tick should be blocked (nil lock) when manual distill holds lock")
	}
}

// ---------------------------------------------------------------------------
// Provider config: per-user, not per-terminal
// ---------------------------------------------------------------------------

func TestProviderConfig_IsPerUser(t *testing.T) {
	// Two "terminals" (goroutines) reading config simultaneously — both must see
	// the same provider since config is per-user file, not per-process/terminal.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "groq")
	t.Setenv("FORGE_API_KEY", "gsk-test")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")
	t.Setenv("GROQ_API_KEY", "")

	const terminals = 4
	providers := make([]string, terminals)
	var wg sync.WaitGroup

	for i := 0; i < terminals; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Simulates each terminal's forge invocation loading config.
			cfg := distill.LoadConfig()
			providers[i] = string(cfg.Provider)
		}()
	}
	wg.Wait()

	for i, p := range providers {
		if p != "groq" {
			t.Errorf("terminal %d sees provider %q; want groq (config must be per-user, not per-terminal)", i, p)
		}
	}
}

// ---------------------------------------------------------------------------
// npm install -g: lock file left by interrupted install
// ---------------------------------------------------------------------------

func TestNpmGlobalInstall_ConcurrentLockNotOurProblem(t *testing.T) {
	// npm manages its own lockfile. Forge must not interfere with it.
	// This test simply verifies that forge's lock files are isolated to ~/.forge/,
	// and a file at npm's typical lock path does not affect forge's distill lock.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate a leftover npm lock file.
	npmLock := filepath.Join(home, ".npm", "_locks", "staging-fake.lock")
	if err := os.MkdirAll(filepath.Dir(npmLock), 0o700); err != nil {
		t.Fatalf("MkdirAll npm lock dir: %v", err)
	}
	if err := os.WriteFile(npmLock, []byte("npm-lock"), 0o600); err != nil {
		t.Fatalf("WriteFile npm lock: %v", err)
	}

	// Forge's distill lock must be in ~/.forge/, completely separate.
	forgeLock := distillLockPath()
	if strings.Contains(forgeLock, ".npm") {
		t.Errorf("distill lock path %q must not be inside .npm/", forgeLock)
	}

	// Acquiring forge's distill lock must work regardless of npm lock presence.
	lock, err := acquireDistillLock()
	if err != nil || lock == nil {
		t.Fatalf("acquireDistillLock failed with npm lock present: err=%v lock=%v", err, lock)
	}
	lock.Close()
	cleanDistillLock()
}

// ---------------------------------------------------------------------------
// Edge: lock dir doesn't exist yet (first run)
// ---------------------------------------------------------------------------

func TestDistillLock_CreatesForgeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Remove .forge entirely — simulates a brand-new install.
	os.RemoveAll(filepath.Join(home, ".forge"))

	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock on fresh install: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock acquired on fresh install")
	}
	lock.Close()
	cleanDistillLock()
}

// ---------------------------------------------------------------------------
// Edge: multiple forge start calls (already covered by existing code, verify
// our lock doesn't interfere with daemon startup lock)
// ---------------------------------------------------------------------------

func TestDistillLock_DoesNotInterfereWithDaemonStartupLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Hold distill lock.
	distillLock, err := acquireDistillLock()
	if err != nil || distillLock == nil {
		t.Fatalf("acquire distill lock: err=%v lock=%v", err, distillLock)
	}
	defer distillLock.Close()
	defer cleanDistillLock()

	// Daemon startup lock must be acquirable independently.
	startupLock, err := acquireStartupLock()
	if err != nil {
		t.Fatalf("daemon startup lock blocked by distill lock (must be independent): %v", err)
	}
	if startupLock == nil {
		t.Fatal("startup lock should succeed while distill lock is held")
	}
	startupLock.Close()
	cleanStartupLock()
}

// ---------------------------------------------------------------------------
// TTL reclaim (#33): a lock older than distillLockTTL must be treated as
// stale even when its PID is technically alive. Protects against leaked
// `forge distill` processes whose PID may still appear alive while the
// owner process is gone (e.g. ephemeral npx temp-path installs) and against
// PID reuse by an unrelated process.
// ---------------------------------------------------------------------------

func TestDistillLock_TTLReclaimEvenWithLivePID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write a PID that is alive (this very test process) but backdate the
	// file mtime past distillLockTTL — simulating a leaked lock that has
	// been wedging the scheduler for 30+ minutes.
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Backdate mtime by 35 minutes — beyond distillLockTTL.
	pastTime := time.Now().Add(-35 * time.Minute)
	if err := os.Chtimes(lockPath, pastTime, pastTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if !isDistillLockStale(lockPath) {
		t.Fatal("lock older than distillLockTTL must be stale even with live PID")
	}

	// acquireDistillLock must clean it and succeed.
	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock after TTL stale: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock acquired after TTL stale cleanup, got nil")
	}
	lock.Close()
	cleanDistillLock()
}

func TestDistillLock_FreshLivePIDNotStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Live current PID + fresh mtime — must NOT be stale.
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if isDistillLockStale(lockPath) {
		t.Fatal("fresh lock with live PID must not be stale")
	}

	// And acquire must refuse (return nil,nil, not steal it).
	lock, err := acquireDistillLock()
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if lock != nil {
		lock.Close()
		cleanDistillLock()
		t.Fatal("must not acquire when fresh live PID holds lock")
	}
	os.Remove(lockPath)
}

// ---------------------------------------------------------------------------
// Wedge visibility (#33): noteDistillSkip must flip to a wedged state after
// 3 consecutive skips; noteDistillCycleRan must reset the counter.
// ---------------------------------------------------------------------------

func TestDistillSkipCounter_WedgesAfterThreeAndResets(t *testing.T) {
	// Start from a clean counter (other tests may have mutated it).
	noteDistillCycleRan()
	if got := distillSkipCount(); got != 0 {
		t.Fatalf("expected skip count 0 after reset, got %d", got)
	}

	noteDistillSkip()
	noteDistillSkip()
	if got := distillSkipCount(); got != 2 {
		t.Fatalf("expected 2 skips, got %d", got)
	}

	// Third skip crosses the wedge threshold; counter goes to 3.
	noteDistillSkip()
	if got := distillSkipCount(); got != 3 {
		t.Fatalf("expected 3 skips at wedge threshold, got %d", got)
	}

	// A real cycle running must reset the counter to 0.
	noteDistillCycleRan()
	if got := distillSkipCount(); got != 0 {
		t.Fatalf("expected 0 skips after cycle ran, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Crash-safe lock release (#51): distillSessionBatch used to release both the
// filesystem distill lock and the in-process distillInProcess mutex only at
// the function's tail, not via defer. A panic partway through a batch — e.g.
// from a provider call — skipped that release entirely and left the
// in-process mutex locked for the rest of the daemon's life, wedging every
// later tick regardless of the filesystem lock's own state.
// ---------------------------------------------------------------------------

func setupWedgeTestSession(t *testing.T, home string) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(home, "wedge.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	base := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	insertDaemonCoachEvent(t, database, "wedge-change", base, "PostToolUse", "Edit", `{"file_path":"a.go"}`)
	insertDaemonCoachEvent(t, database, "wedge-stop", base.Add(time.Minute), "SessionEnd", "", `{}`)
	return database
}

func TestDistillSessionBatch_LockReleasedOnPanic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "anthropic")
	t.Setenv("FORGE_API_KEY", "test-key")

	database := setupWedgeTestSession(t, home)

	orig := synthesizeCheckpointForSession
	t.Cleanup(func() { synthesizeCheckpointForSession = orig })
	synthesizeCheckpointForSession = func(loopD *distill.Distiller, sessionID, proj, checkpointKey string, events []db.Event) (*db.SessionSummary, error) {
		panic("simulated provider crash mid-cycle")
	}

	lastCrossSessionRun := time.Time{}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected distillSessionBatch to panic, it didn't")
			}
		}()
		distillSessionBatch(database, time.Minute, &lastCrossSessionRun)
	}()

	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Errorf("distill lock file should not exist after a panic, stat err = %v", err)
	}

	// The real proof: the in-process mutex must also be free. If the old
	// tail-only release were still in place, this would return (nil, nil)
	// forever — exactly the "scheduler wedged" state from issue #51.
	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock after panic: %v", err)
	}
	if lock == nil {
		t.Fatal("distillInProcess mutex still held after panic — lock release is not crash-safe")
	}
	lock.Close()
	cleanDistillLock()
}

func TestDistillSessionBatch_LockReleasedOnNormalReturn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")

	database := setupWedgeTestSession(t, home)

	lastCrossSessionRun := time.Time{}
	// No provider configured — distillSessionBatch runs its normal control
	// flow (acquire lock, hit the "no inference provider configured" branch
	// per session, return) without touching the panic seam at all.
	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Fatalf("precondition: lock file should not exist yet, stat err = %v", err)
	}
	distillSessionBatch(database, time.Minute, &lastCrossSessionRun)

	if _, err := os.Stat(distillLockPath()); !os.IsNotExist(err) {
		t.Errorf("distill lock file should not exist after a normal return, stat err = %v", err)
	}
	lock, err := acquireDistillLock()
	if err != nil {
		t.Fatalf("acquireDistillLock after normal return: %v", err)
	}
	if lock == nil {
		t.Fatal("distillInProcess mutex still held after normal return")
	}
	lock.Close()
	cleanDistillLock()
}

func TestDistillSessionBatch_NoteDistillCycleRanRunsOnSessionError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")

	database := setupWedgeTestSession(t, home)

	// Force the skip counter into the wedged state, as if prior ticks had
	// been blocked by a competing lock holder. Reset first — this counter is
	// a package-level global other tests in this file also mutate.
	noteDistillCycleRan()
	noteDistillSkip()
	noteDistillSkip()
	noteDistillSkip()
	if got := distillSkipCount(); got != 3 {
		t.Fatalf("precondition: expected skip count 3, got %d", got)
	}

	lastCrossSessionRun := time.Time{}
	// No provider configured, so every session in the batch hits
	// RecordDistillationFailure via the "no inference provider configured"
	// branch — lastErr is non-nil, but the batch still reaches its tail
	// normally (no panic, no early return once the lock is held).
	_, batchErr, _ := distillSessionBatch(database, time.Minute, &lastCrossSessionRun)
	if batchErr == nil {
		t.Fatal("expected a session-level error (no provider configured), got nil")
	}

	if got := distillSkipCount(); got != 0 {
		t.Errorf("expected skip count reset to 0 once a cycle actually ran (even with a session error), got %d", got)
	}
}
