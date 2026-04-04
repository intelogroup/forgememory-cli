package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

// forgeBin is the path to the compiled test binary, set by TestMain.
var forgeBin string

// shortHome creates a short temp dir to avoid Unix socket path length limits
// (104 bytes on macOS). On Unix we use /tmp; on Windows os.TempDir() is fine
// because IPC uses TCP (no socket path length constraint).
func shortHome(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "ft")
	if err != nil {
		t.Fatalf("shortHome: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestMain(m *testing.M) {
	// When invoked as a subprocess for os.Exit capture (see lifecycle_test.go),
	// skip binary build — forgeBin is unused in that context.
	if os.Getenv("FORGE_TEST_SUBPROCESS") != "" {
		os.Exit(m.Run())
	}

	tmp, err := os.MkdirTemp("", "forge-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir for binary: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	forgeBin = filepath.Join(tmp, "forge")
	if runtime.GOOS == "windows" {
		forgeBin = filepath.Join(tmp, "forge.exe")
	}
	// Build from the current directory (cmd/).
	out, err := exec.Command("go", "build", "-o", forgeBin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s\n", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// baseEnv returns os.Environ() without FORGE_* and HOME variables.
// HOME is stripped so callers can set it once without duplicate-key ambiguity
// (POSIX getenv returns the first match, so duplicates would keep the original HOME).
func baseEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "FORGE_") || strings.HasPrefix(e, "HOME=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

// runForge executes the forge binary with the given HOME and args.
// Returns stdout, stderr, and exit code. Fails the test on unexpected errors.
func runForge(t *testing.T, home string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(forgeBin, args...)
	cmd.Env = append(baseEnv(), "HOME="+home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("forge %v: unexpected error: %v", args, err)
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// startTestDaemon starts `forge daemon` directly and registers a cleanup to kill it.
// Waits until forge.addr appears (written after the socket is ready) before returning.
// Waiting on forge.addr — not forge.sock — avoids false positives when a stale socket
// file already exists in the directory.
func startTestDaemon(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(forgeBin, "daemon")
	cmd.Env = append(baseEnv(), "HOME="+home)
	cmd.Stdout = nil
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		// Only log daemon stderr on failure to avoid noise in passing tests.
		if t.Failed() {
			if s := errBuf.String(); s != "" {
				t.Logf("daemon stderr: %s", s)
			}
		}
	})
	addrPath := filepath.Join(home, ".forge", "forge.addr")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(addrPath); err == nil {
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := errBuf.String(); s != "" {
		t.Logf("daemon stderr: %s", s)
	}
	t.Fatalf("daemon did not write addr file within 3s: %s", addrPath)
	return nil
}

// waitForFile polls until path exists or timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file: %s", path)
}

// waitFileGone polls until path is absent or timeout elapses.
func waitFileGone(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file to disappear: %s", path)
}

// ---- basic command dispatch ----

func TestBinary_Version(t *testing.T) {
	home := shortHome(t)
	stdout, _, code := runForge(t, home, "version")
	if code != 0 {
		t.Errorf("forge version: expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "forge ") {
		t.Errorf("forge version output = %q, expected 'forge <version>'", stdout)
	}
}

func TestBinary_NoArgs(t *testing.T) {
	home := shortHome(t)
	_, _, code := runForge(t, home)
	if code != 0 {
		t.Errorf("forge (no args): expected exit 0, got %d", code)
	}
}

func TestBinary_UnknownCommand(t *testing.T) {
	home := shortHome(t)
	_, _, code := runForge(t, home, "notacommand")
	if code != 1 {
		t.Errorf("forge notacommand: expected exit 1, got %d", code)
	}
}

func TestBinary_HelpFlag(t *testing.T) {
	home := shortHome(t)
	for _, flag := range []string{"help", "--help", "-h"} {
		stdout, _, code := runForge(t, home, flag)
		if code != 0 {
			t.Errorf("forge %s: expected exit 0, got %d", flag, code)
		}
		if !strings.Contains(stdout, "forge") {
			t.Errorf("forge %s: expected usage output, got: %q", flag, stdout)
		}
	}
}

func TestBinary_SearchMissingQuery(t *testing.T) {
	home := shortHome(t)
	stdout, _, code := runForge(t, home, "search")
	if code != 1 {
		t.Errorf("forge search (no query): expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout, "Usage") {
		t.Errorf("forge search (no query): expected usage hint, got: %q", stdout)
	}
}

// ---- daemon start/stop lifecycle ----

func TestBinary_StartStop(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	// Start daemon via CLI
	stdout, _, code := runForge(t, home, "start")
	if code != 0 {
		t.Fatalf("forge start: expected exit 0, got %d (stdout: %q)", code, stdout)
	}

	// Daemon is a background grandchild — wait for it to write addr file
	waitForFile(t, addrFile, 3*time.Second)

	// Stop via CLI
	stdout, _, code = runForge(t, home, "stop")
	if code != 0 {
		t.Errorf("forge stop: expected exit 0, got %d (stdout: %q)", code, stdout)
	}

	// Addr file must be gone
	if _, err := os.Stat(addrFile); !os.IsNotExist(err) {
		t.Error("addr file should be removed after forge stop")
	}
}

func TestBinary_DoubleStart(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	// First start
	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)

	// Second start should report already running
	stdout, _, code := runForge(t, home, "start")
	if code != 0 {
		t.Errorf("forge start (second): expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "already running") {
		t.Errorf("second forge start should say 'already running', got: %q", stdout)
	}
}

func TestBinary_DoubleStop(t *testing.T) {
	home := shortHome(t)

	// Stop with no daemon running
	_, _, code := runForge(t, home, "stop")
	if code != 0 {
		t.Errorf("forge stop (no daemon): expected exit 0, got %d", code)
	}

	// Stop again — should be idempotent
	_, _, code = runForge(t, home, "stop")
	if code != 0 {
		t.Errorf("forge stop (second, no daemon): expected exit 0, got %d", code)
	}
}

func TestBinary_RestartCycle(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	// Start → wait → stop
	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)
	runForge(t, home, "stop")

	if _, err := os.Stat(addrFile); !os.IsNotExist(err) {
		t.Fatal("addr file should be gone after stop")
	}

	// Start again — should work even with potentially orphaned daemon from first start
	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)
}

// KNOWN GOTCHA: forge stop removes the addr file but does not send SIGTERM to the
// daemon process. The daemon continues running, consuming the socket and database.
// A subsequent forge start spawns a second daemon — two daemons on different sockets.
func TestBinary_StopOrphan_Gotcha(t *testing.T) {
	home := shortHome(t)

	// Start daemon directly so we have its PID
	daemon := startTestDaemon(t, home)
	pid := daemon.Process.Pid

	// Stop via CLI (removes addr file only)
	runForge(t, home, "stop")

	// Verify the process is still alive (signal 0 = liveness check)
	err := daemon.Process.Signal(syscall.Signal(0))
	if err == nil {
		t.Logf("KNOWN GOTCHA: daemon PID %d still alive after `forge stop` — orphaned", pid)
	} else {
		t.Logf("forge stop killed daemon PID %d (gotcha fixed: %v)", pid, err)
	}

	// Cleanup note: t.Cleanup from startTestDaemon kills it
}

// KNOWN GOTCHA: forge start only checks if the addr file exists, not whether the
// daemon behind it is actually alive. A crashed daemon that didn't clean up its
// addr file will block future starts with "Daemon already running."
func TestBinary_StaleAddr_Gotcha(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	// Simulate a crashed daemon: write addr file without starting any daemon
	if err := os.MkdirAll(filepath.Dir(addrFile), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(addrFile, []byte("stale/path/forge.sock"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stdout, _, code := runForge(t, home, "start")
	if code != 0 {
		t.Errorf("forge start with stale addr: expected exit 0, got %d", code)
	}
	if strings.Contains(stdout, "already running") {
		t.Logf("KNOWN GOTCHA: stale addr file causes forge start to report 'already running' — no liveness check performed")
	} else {
		t.Logf("forge start correctly started despite stale addr file (gotcha fixed)")
	}
}

// ---- daemon SIGTERM handling ----

func TestBinary_DaemonSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not supported on Windows")
	}
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	daemon := startTestDaemon(t, home)

	// Verify addr file exists
	if _, err := os.Stat(addrFile); err != nil {
		t.Fatalf("addr file should exist after daemon start: %v", err)
	}

	// Send SIGTERM
	if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	// Daemon should clean up addr file on graceful shutdown
	waitFileGone(t, addrFile, 3*time.Second)
}

func TestBinary_DaemonSIGINT(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT not supported on Windows")
	}
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	daemon := startTestDaemon(t, home)

	if err := daemon.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("SIGINT: %v", err)
	}

	waitFileGone(t, addrFile, 3*time.Second)
}

// ---- hook lifecycle ----

func TestBinary_Hook_DaemonDown(t *testing.T) {
	home := shortHome(t)

	// Run hook with no daemon — must exit 0 (silent failure contract)
	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=claude",
		"FORGE_EVENT_TYPE=PostToolUse",
	)
	cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","hook_event_name":"PostToolUse"}`)
	err := cmd.Run()
	if err != nil {
		t.Errorf("forge hook with no daemon must exit 0 (silent failure), got: %v", err)
	}

	// Verify no event was written to DB (DB shouldn't even exist)
	dbPath := filepath.Join(home, ".forge", "forge.db")
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		// DB was created by a previous command — check event count is 0
		database, err := db.Open(dbPath)
		if err == nil {
			total, _, _ := database.EventCount()
			database.Close()
			if total != 0 {
				t.Errorf("expected 0 events with daemon down, got %d", total)
			}
		}
	}
}

func TestBinary_Hook_DaemonUp(t *testing.T) {
	home := shortHome(t)
	startTestDaemon(t, home)

	// Send a hook event
	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=claude",
		"FORGE_EVENT_TYPE=PostToolUse",
		"FORGE_SESSION_ID=test-session-123",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"test-session-123","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"echo hi"}}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("forge hook: %v", err)
	}

	// Poll DB until event appears (daemon handles it asynchronously)
	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		database, err := db.Open(dbPath)
		if err == nil {
			total, _, _ := database.EventCount()
			database.Close()
			if total > 0 {
				return // success
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("hook event did not appear in DB within 2s")
}

func TestBinary_Hook_ConcurrentHooks(t *testing.T) {
	const nHooks = 10
	home := shortHome(t)
	startTestDaemon(t, home)

	var wg sync.WaitGroup
	for i := 0; i < nHooks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(forgeBin, "hook")
			cmd.Env = append(baseEnv(),
				"HOME="+home,
				"FORGE_SOURCE_TOOL=claude",
				"FORGE_EVENT_TYPE=PostToolUse",
				fmt.Sprintf("FORGE_SESSION_ID=concurrent-session-%d", i),
			)
			cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"session_id":"concurrent-session-%d","tool_name":"Bash"}`, i))
			cmd.Run()
		}(i)
	}
	wg.Wait()

	// Poll until all events arrive (daemon processes them asynchronously)
	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		database, err := db.Open(dbPath)
		if err == nil {
			total, _, _ := database.EventCount()
			database.Close()
			if total >= nHooks {
				return // success
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	total, _, _ := database.EventCount()
	database.Close()
	t.Errorf("expected %d events after concurrent hooks, got %d", nHooks, total)
}

// ---- socket / IPC edge cases ----

func TestBinary_StaleSocketCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket cleanup not applicable on Windows (IPC uses TCP)")
	}
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write a fake stale socket file
	staleSock := filepath.Join(forgeDir, "forge.sock")
	if err := os.WriteFile(staleSock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Daemon should start successfully despite stale socket (ipc.Listen removes it)
	startTestDaemon(t, home)

	// Verify socket was replaced (it's now a real socket, not a plain file)
	info, err := os.Stat(staleSock)
	if err != nil {
		t.Fatalf("socket file should exist after daemon start: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Error("socket file should be a Unix socket after daemon start, not a plain file")
	}
}

// ---- init idempotency ----

func TestBinary_Init_Idempotent(t *testing.T) {
	home := shortHome(t)

	// First init
	_, _, code := runForge(t, home, "init")
	if code != 0 {
		t.Errorf("forge init (first): expected exit 0, got %d", code)
	}

	// Second init — should not corrupt DB or error
	_, _, code = runForge(t, home, "init")
	if code != 0 {
		t.Errorf("forge init (second): expected exit 0, got %d", code)
	}

	// DB should still be valid
	dbPath := filepath.Join(home, ".forge", "forge.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open after double init: %v", err)
	}
	database.Close()
}

// ---- status and doctor with live daemon ----

func TestBinary_Status_DaemonUp(t *testing.T) {
	home := shortHome(t)
	startTestDaemon(t, home)

	stdout, _, code := runForge(t, home, "status")
	if code != 0 {
		t.Errorf("forge status: expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("forge status should show daemon running, got: %q", stdout)
	}
}

func TestBinary_Doctor_DaemonUp(t *testing.T) {
	home := shortHome(t)
	startTestDaemon(t, home)

	stdout, _, code := runForge(t, home, "doctor")
	if code != 0 {
		t.Errorf("forge doctor: expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "[OK] Daemon") {
		t.Errorf("forge doctor should show OK daemon, got: %q", stdout)
	}
}

// ---- concurrent start serialization ----

// TestBinary_ConcurrentStart verifies that concurrent start calls serialize
// around the bootstrap path instead of racing to spawn multiple daemons.
func TestBinary_ConcurrentStart(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	var results []struct {
		stdout string
		code   int
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, _, code := runForge(t, home, "start")
			mu.Lock()
			results = append(results, struct {
				stdout string
				code   int
			}{stdout: stdout, code: code})
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Cleanup(func() { runForge(t, home, "stop") })

	waitForFile(t, addrFile, 3*time.Second)
	for _, result := range results {
		if result.code != 0 {
			t.Fatalf("concurrent forge start exited %d (stdout: %q)", result.code, result.stdout)
		}
	}

	statusOut, _, _ := runForge(t, home, "status")
	if !strings.Contains(statusOut, "running") {
		t.Fatalf("daemon should be running after concurrent starts, got: %q", statusOut)
	}

	secondStart, _, code := runForge(t, home, "start")
	if code != 0 {
		t.Fatalf("post-bootstrap forge start exited %d", code)
	}
	if !strings.Contains(secondStart, "already running") {
		t.Fatalf("expected follow-up start to report already running, got: %q", secondStart)
	}
}

// ---- stale state: status and doctor report failure consistently ----

// TestBinary_Status_StaleAddr verifies that `forge status` reports "stale" when
// an addr file exists but nothing is listening on the recorded address.
func TestBinary_Status_StaleAddr(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(forgeDir, "forge.addr"), []byte("127.0.0.1:1"), 0o600); err != nil {
		t.Fatalf("WriteFile stale addr: %v", err)
	}

	stdout, _, _ := runForge(t, home, "status")
	if !strings.Contains(stdout, "stale") {
		t.Errorf("forge status with stale addr should contain 'stale', got: %q", stdout)
	}
}

// TestBinary_Doctor_StaleAddr verifies that `forge doctor` reports [FAIL] Daemon
// when an addr file exists but the daemon is not responding.
func TestBinary_Doctor_StaleAddr(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(forgeDir, "forge.addr"), []byte("127.0.0.1:1"), 0o600); err != nil {
		t.Fatalf("WriteFile stale addr: %v", err)
	}

	stdout, _, _ := runForge(t, home, "doctor")
	if !strings.Contains(stdout, "[FAIL] Daemon") {
		t.Errorf("forge doctor with stale addr should contain '[FAIL] Daemon', got: %q", stdout)
	}
}

// TestBinary_Start_ClearsStaleState verifies that `forge start` clears both a
// stale addr file and a stale pid file before starting a fresh daemon.
func TestBinary_Start_ClearsStaleState(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Simulate a crashed daemon: stale addr at a port nothing is listening on,
	// plus an orphaned pid file.
	staleAddr := []byte("127.0.0.1:1")
	if err := os.WriteFile(filepath.Join(forgeDir, "forge.addr"), staleAddr, 0o600); err != nil {
		t.Fatalf("WriteFile stale addr: %v", err)
	}
	if err := os.WriteFile(filepath.Join(forgeDir, "forge.pid"), []byte("99999"), 0o600); err != nil {
		t.Fatalf("WriteFile stale pid: %v", err)
	}

	stdout, _, code := runForge(t, home, "start")
	if code != 0 {
		t.Fatalf("forge start over stale state: expected exit 0, got %d (stdout: %q)", code, stdout)
	}
	t.Cleanup(func() { runForge(t, home, "stop") })

	// After start, the addr must have changed (stale addr was cleared, new one written).
	newAddr, _ := os.ReadFile(filepath.Join(forgeDir, "forge.addr"))
	if string(newAddr) == string(staleAddr) {
		t.Error("stale addr was not replaced: forge start should have cleared it and written a fresh one")
	}

	// Status must show daemon running, not stale.
	statusOut, _, _ := runForge(t, home, "status")
	if !strings.Contains(statusOut, "running") {
		t.Errorf("forge status after start-over-stale should show running, got: %q", statusOut)
	}
}

// TestBinary_Doctor_ShowsLogPath verifies that `forge doctor` prints the daemon
// log file path once `forge start` has created it.
func TestBinary_Doctor_ShowsLogPath(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)
	t.Cleanup(func() { runForge(t, home, "stop") })

	stdout, _, _ := runForge(t, home, "doctor")
	logPath := filepath.Join(home, ".forge", "daemon.log")
	if !strings.Contains(stdout, logPath) {
		t.Errorf("forge doctor should show daemon log path %s, got: %q", logPath, stdout)
	}
}

// TestBinary_Upgrade_PreservesEvents verifies that running `forge init` a second
// time does not wipe the existing database or corrupt existing event records.
func TestBinary_Upgrade_PreservesEvents(t *testing.T) {
	home := shortHome(t)

	// First init + start daemon to accept hook events.
	runForge(t, home, "init")
	addrFile := filepath.Join(home, ".forge", "forge.addr")
	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)

	// Send an event through the hook.
	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=claude",
		"FORGE_EVENT_TYPE=PostToolUse",
		"FORGE_SESSION_ID=upgrade-test-session",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"upgrade-test-session","tool_name":"Bash"}`)
	cmd.Run()

	// Wait for event to land in DB.
	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if database, err := db.Open(dbPath); err == nil {
			total, _, _ := database.EventCount()
			database.Close()
			if total > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	runForge(t, home, "stop")

	// Record event count before second init.
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open before second init: %v", err)
	}
	countBefore, _, _ := database.EventCount()
	database.Close()

	// Second init — must not wipe or corrupt the DB.
	_, _, code := runForge(t, home, "init")
	if code != 0 {
		t.Fatalf("forge init (second): expected exit 0, got %d", code)
	}

	database, err = db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open after second init: %v", err)
	}
	countAfter, _, _ := database.EventCount()
	database.Close()

	if countAfter < countBefore {
		t.Errorf("second forge init reduced event count from %d to %d — DB was corrupted or wiped", countBefore, countAfter)
	}
}

// TestBinary_DaemonLockFile verifies that the daemon creates a lock file
// to prevent multiple daemons from running simultaneously.
func TestBinary_DaemonLockFile(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Start first daemon
	runForge(t, home, "start")
	waitForFile(t, filepath.Join(forgeDir, "forge.lock"), 3*time.Second)

	// Check lock file exists
	lockData, err := os.ReadFile(filepath.Join(forgeDir, "forge.lock"))
	if err != nil {
		t.Fatalf("lock file should exist after daemon start: %v", err)
	}

	// Verify lock file contains PID
	if len(lockData) == 0 {
		t.Error("lock file should not be empty")
	}

	// Stop daemon
	runForge(t, home, "stop")

	// Verify lock file is removed after stop
	_, err = os.Stat(filepath.Join(forgeDir, "forge.lock"))
	if !os.IsNotExist(err) {
		t.Error("lock file should be removed after daemon stop")
	}
}

// TestBinary_SingletonDaemon verifies that only one daemon can run at a time.
func TestBinary_SingletonDaemon(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Start first daemon
	runForge(t, home, "start")
	waitForFile(t, filepath.Join(forgeDir, "forge.lock"), 3*time.Second)

	// Verify daemon is running
	stdout, _, _ := runForge(t, home, "status")
	if !strings.Contains(stdout, "running") {
		t.Errorf("daemon should be running, got: %s", stdout)
	}

	// Verify second start reports already running
	stdout, _, _ = runForge(t, home, "start")
	if !strings.Contains(stdout, "already running") {
		t.Errorf("second start should report already running, got: %s", stdout)
	}

	// Stop daemon
	runForge(t, home, "stop")

	// Verify daemon is stopped
	stdout, _, _ = runForge(t, home, "status")
	if !strings.Contains(stdout, "not running") {
		t.Errorf("daemon should be stopped, got: %s", stdout)
	}
}
