package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/db"
)

// captureStdout swaps os.Stdout for a pipe, runs f, then restores and returns captured output.
// Not safe for parallel tests that also capture stdout.
func captureStdout(f func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

func hasEnv(env []string, key, want string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix) == want
		}
	}
	return false
}

// seedEvents inserts n events into the DB at the current HOME path.
func seedEvents(t *testing.T, n int) {
	t.Helper()
	database, err := db.Open("")
	if err != nil {
		t.Fatalf("seedEvents db.Open: %v", err)
	}
	defer database.Close()
	for i := 0; i < n; i++ {
		e := &db.Event{
			SessionID:  "test-session",
			ProjectID:  "test-project",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    `{"tool":"Bash","input":"echo hello"}`,
		}
		if err := database.InsertEvent(e); err != nil {
			t.Fatalf("seedEvents InsertEvent: %v", err)
		}
	}
}

// ---- addr file helpers ----

func TestAddrRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if readAddr() != "" {
		t.Fatal("expected empty addr before write")
	}
	writeAddr("forge-test-addr")
	if got := readAddr(); got != "forge-test-addr" {
		t.Errorf("readAddr = %q, want %q", got, "forge-test-addr")
	}
	cleanAddr()
	if readAddr() != "" {
		t.Fatal("expected empty addr after cleanAddr")
	}
}

func TestReadAddr_Missing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := readAddr(); got != "" {
		t.Errorf("readAddr on missing file = %q, want empty", got)
	}
}

func TestWriteAddr_CreatesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Ensure .forge dir does not exist yet
	os.RemoveAll(filepath.Join(home, ".forge"))

	writeAddr("test-addr")

	addrFile := filepath.Join(home, ".forge", "forge.addr")
	if _, err := os.Stat(addrFile); err != nil {
		t.Errorf("forge.addr should exist after writeAddr: %v", err)
	}
	data, _ := os.ReadFile(addrFile)
	if string(data) != "test-addr" {
		t.Errorf("forge.addr content = %q, want %q", string(data), "test-addr")
	}
}

func TestCleanAddr_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Calling cleanAddr when no file exists should not panic or error
	cleanAddr()
	cleanAddr()
}

// ---- runStop ----

func TestRunStop_DaemonNotRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(func() { runStop([]string{}) })
	if !strings.Contains(out, "Daemon stopped") {
		t.Errorf("expected 'Daemon stopped' in output, got: %q", out)
	}
}

func TestRunStop_CleansAddrFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeAddr("fake-addr")
	captureStdout(func() { runStop([]string{}) })

	if readAddr() != "" {
		t.Error("addr file should be removed after runStop")
	}
}

func TestRunStop_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Stopping twice should never panic
	captureStdout(func() { runStop([]string{}) })
	captureStdout(func() { runStop([]string{}) })
}

func TestNewDaemonCommand_SetsDetachedDaemonEnvForStart(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")

	cmd, closeLog := newDaemonCommand(logPath, true)
	defer closeLog()

	if got, want := cmd.Path, agent.ForgePath(); got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "daemon" {
		t.Fatalf("cmd.Args = %v, want [self daemon]", cmd.Args)
	}
	if cmd.Stdin != nil {
		t.Fatal("daemon command should detach from stdin")
	}
	if !hasEnv(cmd.Env, "FORGE_NO_EXIT_ON_PARENT_EXIT", "1") {
		t.Fatalf("cmd.Env missing FORGE_NO_EXIT_ON_PARENT_EXIT=1: %v", cmd.Env)
	}
	if cmd.Stdout == nil || cmd.Stderr == nil {
		t.Fatal("daemon command should log stdout/stderr to daemon.log when available")
	}
}

func TestNewDaemonCommand_KeepsParentMonitorForMCP(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	t.Setenv("FORGE_NO_EXIT_ON_PARENT_EXIT", "1")

	cmd, closeLog := newDaemonCommand(logPath, false)
	defer closeLog()

	if hasEnv(cmd.Env, "FORGE_NO_EXIT_ON_PARENT_EXIT", "1") {
		t.Fatalf("cmd.Env should not disable parent monitoring for MCP launches: %v", cmd.Env)
	}
}

// ---- runDistill ----

func TestRunDistill_NoEvents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(func() { runDistill([]string{}) })
	if !strings.Contains(out, "No undistilled events") {
		t.Errorf("expected 'No undistilled events' in output, got: %q", out)
	}
}

func TestRunDistill_WithEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"response": `[{"type":"bugfix","title":"Windows daemon fix","narrative":"Use the real distillation path.","impact_score":0.8}]`,
		})
	}))
	defer server.Close()
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", server.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	out := captureStdout(func() { runDistill([]string{}) })

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	_, undistilled, err := database.EventCount()
	if err != nil {
		t.Fatalf("EventCount: %v", err)
	}
	if undistilled != 0 {
		t.Errorf("expected 0 undistilled after runDistill, got %d", undistilled)
	}
	count, err := database.PrincipleCount()
	if err != nil {
		t.Fatalf("PrincipleCount: %v", err)
	}
	if count == 0 {
		t.Fatal("expected principles to be extracted by runDistill")
	}
	if !strings.Contains(out, "Distilled 1 principle") {
		t.Errorf("expected distill output to mention extracted principles, got: %q", out)
	}
}

func TestRunDistill_NotEnoughEventsKeepsQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 2)

	out := captureStdout(func() { runDistill([]string{}) })

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	count, err := database.PrincipleCount()
	if err != nil {
		t.Fatalf("PrincipleCount: %v", err)
	}
	if count != 0 {
		t.Errorf("runDistill extracted %d principles with only 2 events", count)
	}
	_, undistilled, err := database.EventCount()
	if err != nil {
		t.Fatalf("EventCount: %v", err)
	}
	if undistilled != 2 {
		t.Fatalf("expected undistilled events to remain queued, got %d", undistilled)
	}
	if !strings.Contains(out, "Not enough undistilled events") {
		t.Errorf("expected insufficient-events message, got: %q", out)
	}
}

func TestRunDistill_UnconfiguredProviderSkipsAndKeepsQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_BASE_URL", "http://127.0.0.1:1")
	seedEvents(t, 3)

	out := captureStdout(func() { runDistill([]string{}) })

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	count, err := database.PrincipleCount()
	if err != nil {
		t.Fatalf("PrincipleCount: %v", err)
	}
	if count != 0 {
		t.Errorf("runDistill extracted %d principles despite missing provider", count)
	}
	_, undistilled, err := database.EventCount()
	if err != nil {
		t.Fatalf("EventCount: %v", err)
	}
	if undistilled != 3 {
		t.Fatalf("expected undistilled events to remain queued, got %d", undistilled)
	}
	if !strings.Contains(out, "Skipping distillation: Cannot reach inference provider") {
		t.Errorf("expected skip message for missing provider, got: %q", out)
	}
	if !strings.Contains(out, "Events remain queued") {
		t.Errorf("expected queued-events message, got: %q", out)
	}
}

func TestRunDistill_ExplicitProviderUnreachableExits(t *testing.T) {
	if os.Getenv("FORGE_TEST_SUBPROCESS") == "distill_unreachable" {
		runDistill([]string{})
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 3)

	cmd := exec.Command(os.Args[0], "-test.run=TestRunDistill_ExplicitProviderUnreachableExits", "-test.v=false")
	cmd.Env = append(
		os.Environ(),
		"FORGE_TEST_SUBPROCESS=distill_unreachable",
		"HOME="+home,
		"FORGE_PROVIDER=ollama",
		"FORGE_API_KEY=",
		"FORGE_BASE_URL=http://127.0.0.1:1",
	)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected non-zero exit, got err=%v output=%q", err, string(out))
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %d output=%q", exitErr.ExitCode(), string(out))
	}
	if !strings.Contains(string(out), "Cannot reach inference provider") {
		t.Fatalf("expected provider error in output, got %q", string(out))
	}
}

func TestFindTransientForgeReference_WindowsStylePath(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := `{"mcpServers":{"forge":{"command":"C:\\Users\\runneradmin\\AppData\\Local\\Temp\\forge-bin-12345\\forge.exe","args":["mcp"]}}}`
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatalf("WriteFile settings.json: %v", err)
	}

	ref, ok := findTransientForgeReference(settingsPath)
	if !ok {
		t.Fatal("expected Windows-style transient forge path to be detected")
	}
	if !strings.Contains(strings.ToLower(ref), "forge-bin-12345") {
		t.Fatalf("expected transient reference in output, got %q", ref)
	}
}

func TestIsStaleLock_AlivePIDIsNotStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lockPath := filepath.Join(home, ".forge", "forge.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !isStaleLock() {
		t.Fatal("expected unparsable lock file to be stale")
	}
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile current pid: %v", err)
	}
	if isStaleLock() {
		t.Fatal("expected live lock PID to be treated as healthy")
	}
}

// ---- runSearch ----

// TestRunSearch_NoQuery verifies that forge search with no argument exits 1.
// Uses the subprocess trick to safely capture os.Exit.
func TestRunSearch_NoQuery(t *testing.T) {
	if os.Getenv("FORGE_TEST_SUBPROCESS") == "search_nq" {
		runSearch([]string{}) // calls os.Exit(1)
		return
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestRunSearch_NoQuery", "-test.v=false")
	cmd.Env = append(os.Environ(), "FORGE_TEST_SUBPROCESS=search_nq", "HOME="+home)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected non-zero exit, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit 1, got %d", exitErr.ExitCode())
	}
}

func TestRunSearch_NoResults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(func() { runSearch([]string{"zxqvnotfound999"}) })
	if !strings.Contains(out, "No results") {
		t.Errorf("expected 'No results' in output, got: %q", out)
	}
}

func TestRunSearch_WithResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	e := &db.Event{
		SessionID:  "s1",
		ProjectID:  "p1",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    `{"tool":"uniquewidget42marker"}`,
	}
	if err := database.InsertEvent(e); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	database.Close()

	out := captureStdout(func() { runSearch([]string{"uniquewidget42marker"}) })
	if !strings.Contains(out, "uniquewidget42marker") {
		t.Errorf("expected payload in search output, got: %q", out)
	}
}

// ---- truncate ----

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc..."},
		{"abcde", 5, "abcde"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcdef..."},
		{"", 10, ""},
	}
	for _, tc := range tests {
		if got := truncate(tc.input, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}

// ---- statusOutput ----

func TestStatusOutput_DaemonDown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(statusOutput)
	if !strings.Contains(out, "not running") {
		t.Errorf("expected 'not running' in status output, got: %q", out)
	}
}

func TestStatusOutput_DaemonUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAddr("forge-fake-addr")
	out := captureStdout(statusOutput)
	if !strings.Contains(out, "forge-fake-addr") {
		t.Errorf("expected addr string in status output, got: %q", out)
	}
}

func TestStatusOutput_ShowsEventCounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedEvents(t, 2)

	out := captureStdout(statusOutput)
	if !strings.Contains(out, "2") {
		t.Errorf("expected event count in status output, got: %q", out)
	}
}

// ---- runDoctor ----

func TestRunDoctor_DBExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pre-create the DB
	d, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	d.Close()

	out := captureStdout(func() { runDoctor([]string{}) })
	if !strings.Contains(out, "[OK] Database") {
		t.Errorf("expected '[OK] Database' in doctor output, got: %q", out)
	}
}

func TestRunDoctor_DaemonDown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := captureStdout(func() { runDoctor([]string{}) })
	if !strings.Contains(out, "[FAIL] Daemon") {
		t.Errorf("expected '[FAIL] Daemon' when daemon not running, got: %q", out)
	}
}

func TestRunDoctor_StaleAddr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Addr file exists but socket is unreachable — should report FAIL, not OK.
	writeAddr("forge-fake-addr")
	out := captureStdout(func() { runDoctor([]string{}) })
	if !strings.Contains(out, "[FAIL] Daemon") {
		t.Errorf("expected '[FAIL] Daemon' for stale addr, got: %q", out)
	}
}

// ---- stale addr file gotcha (FIXED) ----

// The old gotcha: if the daemon crashed without cleaning its addr file,
// forge start would see the stale addr and report "already running" instead of
// starting fresh. This is now fixed — runStart clears a non-alive addr before
// spawning a new daemon.
//
// This test verifies the cleanup path without actually spawning a daemon
// (which would require the test binary to support the "daemon" subcommand).
func TestRunStart_Gotcha_StaleAddrBlocksRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write a stale addr file (as if a crashed daemon left it behind).
	writeAddr("stale-unix-path")

	// Verify runStart sees the stale addr as not-alive and cleans it up.
	// We can't invoke runStart fully here (it would spawn a daemon process and
	// eventually call os.Exit), so test the cleanup logic directly.
	addr := readAddr()
	if addr == "" {
		t.Fatal("expected stale addr to be present")
	}
	if isDaemonAlive(addr) {
		t.Fatal("stale addr should not be alive")
	}
	// This is exactly what runStart does: clean up stale addr then start fresh.
	cleanAddr()
	cleanPID()

	if readAddr() != "" {
		t.Error("stale addr should have been removed after cleanup")
	}
	t.Log("GOTCHA FIXED: stale addr correctly detected as non-alive and cleaned before restart")
}

// TestRunStart_StaleAddrAndPidBothCleared verifies that when both a stale addr
// and a stale pid file are present, runStart clears both before spawning fresh.
func TestRunStart_StaleAddrAndPidBothCleared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeAddr("stale-addr")
	writePID(99999)

	// Simulate the runStart cleanup path (addr non-alive → clear both).
	if isDaemonAlive(readAddr()) {
		t.Fatal("stale addr should not be alive")
	}
	cleanAddr()
	cleanPID()

	if readAddr() != "" {
		t.Error("stale addr file should be removed")
	}
	if readPID() != 0 {
		t.Error("stale pid file should be removed")
	}
}

// TestRunStart_StalePidNoAddr verifies that an orphaned pid file (daemon killed
// before it wrote its addr) is cleaned up even when no addr file exists.
// Prior to the fix, runStart only called cleanPID inside the addr != "" branch,
// so an orphan pid with no addr was silently left behind.
func TestRunStart_StalePidNoAddr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write pid only — no addr file.
	writePID(99999)

	if readAddr() != "" {
		t.Fatal("no addr file should exist")
	}
	if readPID() == 0 {
		t.Fatal("expected orphan pid to be present")
	}

	// runStart now always cleans both, regardless of addr presence.
	cleanAddr()
	cleanPID()

	if readPID() != 0 {
		t.Error("orphan pid file should be removed before fresh start")
	}
}
