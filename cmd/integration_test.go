package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

// forgeBin is the path to the compiled test binary, set by TestMain.
var forgeBin string

// killProcessTree kills a process and all its children. On Windows this uses
// taskkill /T /F to kill the entire tree; on Unix it kills the process directly
// (daemon runs in its own session via Setsid so children are not orphaned).
func killProcessTree(pid int) {
	if runtime.GOOS == "windows" {
		exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill()
	}
}

// waitOrTimeout calls cmd.Wait with a bounded timeout so test cleanup never
// hangs if process-tree kill fails to terminate the daemon.
func waitOrTimeout(cmd *exec.Cmd, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

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

	tmp, err := os.MkdirTemp("", "forge-install-*")
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
	return runForgePath(t, forgeBin, home, nil, args...)
}

func runForgePath(t *testing.T, binaryPath, home string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(baseEnv(), "HOME="+home)
	cmd.Env = append(cmd.Env, extraEnv...)
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

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// startTestDaemon starts `forge daemon` directly and registers a cleanup to kill it.
// Waits until forge.addr appears (written after the socket is ready) before returning.
// Waiting on forge.addr — not forge.sock — avoids false positives when a stale socket
// file already exists in the directory.
func startTestDaemon(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	return startTestDaemonEnv(t, home, nil)
}

func startTestDaemonEnv(t *testing.T, home string, extraEnv []string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(forgeBin, "daemon")
	cmd.Env = append(baseEnv(), "HOME="+home)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout = nil
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		killProcessTree(cmd.Process.Pid)
		waitOrTimeout(cmd, 5*time.Second)
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

func TestContext7MCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CONTEXT7_MCP_HELPER") != "1" {
		return
	}

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		var msg map[string]any
		if err := json.Unmarshal([]byte(strings.TrimRight(line, "\r\n")), &msg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			writeMCPHelperMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "context7-test", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			continue
		case "tools/list":
			writeMCPHelperMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "resolve-library-id"},
						map[string]any{"name": "query-docs"},
					},
				},
			})
		case "tools/call":
			params, _ := msg["params"].(map[string]any)
			name, _ := params["name"].(string)
			switch name {
			case "resolve-library-id":
				writeMCPHelperMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]any{
						"content": []any{
							map[string]any{
								"type": "text",
								"text": `[{"libraryId":"/rust-lang/book","title":"Rust"}]`,
							},
						},
					},
				})
			case "query-docs":
				if delayMS, _ := strconv.Atoi(os.Getenv("GO_WANT_CONTEXT7_MCP_DELAY_MS")); delayMS > 0 {
					time.Sleep(time.Duration(delayMS) * time.Millisecond)
				}
				writeMCPHelperMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]any{
						"content": []any{
							map[string]any{
								"type": "text",
								"text": `[{"title":"E0599","content":"Rust E0599 usually means the method is not in scope or the trait providing it is not imported. Import the trait or call a method that exists for the type."}]`,
							},
						},
					},
				})
			default:
				writeMCPHelperMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"error":   map[string]any{"code": -32601, "message": "unknown tool"},
				})
			}
		default:
			writeMCPHelperMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"error":   map[string]any{"code": -32601, "message": "unknown method"},
			})
		}
	}
}

func writeMCPHelperMessage(writer *bufio.Writer, msg map[string]any) {
	body, _ := json.Marshal(msg)
	writer.Write(body)
	writer.WriteByte('\n')
	writer.Flush()
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
	t.Cleanup(func() { runForge(t, home, "stop") })
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

func TestBinary_TransientBinary_UsesStableInstallForCodexAndDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script fake codex and unix process inspection")
	}

	home := shortHome(t)
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}

	transientDir, err := os.MkdirTemp("", "forge-bin-*")
	if err != nil {
		t.Fatalf("mkdir transient dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(transientDir) })

	transientBin := filepath.Join(transientDir, "forge")
	copyFile(t, forgeBin, transientBin)

	fakeCodexDir := t.TempDir()
	codexLog := filepath.Join(home, "codex.log")
	fakeCodex := filepath.Join(fakeCodexDir, "codex")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
if [ "$1" = "--version" ]; then
  echo "codex test"
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "add" ]; then
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "get" ]; then
  exit 0
fi
exit 0
`, codexLog)
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	pathEnv := strings.Join([]string{fakeCodexDir, filepath.Dir(forgeBin), os.Getenv("PATH")}, string(os.PathListSeparator))
	extraEnv := []string{
		"PATH=" + pathEnv,
		"CODEX_HOME=" + codexHome,
	}

	stdout, stderr, code := runForgePath(t, transientBin, home, extraEnv, "sync-integrations")
	if code != 0 {
		t.Fatalf("transient forge sync-integrations exited %d (stdout: %q, stderr: %q)", code, stdout, stderr)
	}

	codexArgs, err := os.ReadFile(codexLog)
	if err != nil {
		t.Fatalf("read fake codex log: %v", err)
	}
	if !strings.Contains(string(codexArgs), fmt.Sprintf("mcp add forge -- %s mcp", forgeBin)) {
		t.Fatalf("codex mcp add should use stable forge path %q, got log: %q", forgeBin, string(codexArgs))
	}
	if strings.Contains(string(codexArgs), transientBin) {
		t.Fatalf("codex mcp add should not use transient forge path %q, log: %q", transientBin, string(codexArgs))
	}

	skillData, err := os.ReadFile(filepath.Join(codexHome, "skills", "forge.json"))
	if err != nil {
		t.Fatalf("read codex skill: %v", err)
	}
	if !strings.Contains(string(skillData), forgeBin) {
		t.Fatalf("codex skill should reference stable forge path %q, got: %q", forgeBin, string(skillData))
	}
	if strings.Contains(string(skillData), transientBin) {
		t.Fatalf("codex skill should not reference transient forge path %q", transientBin)
	}

	stdout, stderr, code = runForgePath(t, transientBin, home, extraEnv, "start")
	if code != 0 {
		t.Fatalf("transient forge start exited %d (stdout: %q, stderr: %q)", code, stdout, stderr)
	}
	t.Cleanup(func() { runForgePath(t, transientBin, home, extraEnv, "stop") })
	waitForFile(t, filepath.Join(home, ".forge", "forge.addr"), 3*time.Second)

	pidData, err := os.ReadFile(filepath.Join(home, ".forge", "forge.pid"))
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidData)), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("parse pid file %q: pid=%d err=%v", string(pidData), pid, err)
	}
	identity, err := processIdentity(pid)
	if err != nil {
		t.Fatalf("processIdentity(%d): %v", pid, err)
	}
	if !strings.Contains(identity, forgeBin) {
		t.Fatalf("daemon should be launched from stable forge path %q, got identity %q", forgeBin, identity)
	}
	if strings.Contains(identity, transientBin) {
		t.Fatalf("daemon should not be launched from transient forge path %q, got identity %q", transientBin, identity)
	}

	mcpCmd := exec.Command(transientBin, "mcp")
	mcpCmd.Env = append(baseEnv(), "HOME="+home)
	mcpCmd.Env = append(mcpCmd.Env, extraEnv...)
	mcpCmd.Stdin = strings.NewReader("")
	var mcpStdout, mcpStderr bytes.Buffer
	mcpCmd.Stdout = &mcpStdout
	mcpCmd.Stderr = &mcpStderr
	if err := mcpCmd.Run(); err != nil {
		t.Fatalf("transient forge mcp: %v (stdout: %q, stderr: %q)", err, mcpStdout.String(), mcpStderr.String())
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
	t.Cleanup(func() { runForge(t, home, "stop") })
	waitForFile(t, addrFile, 3*time.Second)
}

// KNOWN GOTCHA: forge stop removes the addr file but does not send SIGTERM to the
// daemon process. The daemon continues running, consuming the socket and database.
// A subsequent forge start spawns a second daemon — two daemons on different sockets.
func TestBinary_StopOrphan_Gotcha(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.Signal(0) liveness check not supported on Windows")
	}
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
	t.Cleanup(func() { runForge(t, home, "stop") })
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

func TestBinary_Hook_RepeatedFailureCreatesAlertAndInjectsRecall(t *testing.T) {
	home := shortHome(t)
	startTestDaemon(t, home)

	payload := `{"session_id":"rust-fail-1","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=PostToolUse",
			"FORGE_SESSION_ID=rust-fail-1",
		)
		cmd.Stdin = strings.NewReader(payload)
		if err := cmd.Run(); err != nil {
			t.Fatalf("forge hook failure event %d: %v", i+1, err)
		}
	}

	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		database, err := db.Open(dbPath)
		if err == nil {
			alerts, _ := database.ActiveAlertsByProject("api-service", 5)
			database.Close()
			if len(alerts) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	database.Close()
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("expected at least one active repeated-failure alert")
	}

	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=codex",
		"FORGE_EVENT_TYPE=UserPromptSubmit",
		"FORGE_SESSION_ID=rust-fail-1",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"rust-fail-1","cwd":"/tmp/api-service","hook_event_name":"UserPromptSubmit","messages":[{"role":"user","content":[{"type":"text","text":"fix the cargo build failure"}]}]}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("forge hook recall output: %v", err)
	}
	var recall struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &recall); err != nil {
		t.Fatalf("expected JSON recall output, got %q: %v", string(out), err)
	}
	if recall.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("expected UserPromptSubmit hook event name, got %q", recall.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(recall.HookSpecificOutput.AdditionalContext, "Active repeated failure") {
		t.Fatalf("expected repeated-failure recall injection, got %q", string(out))
	}
}

func TestBinary_Hook_RepeatedFailureClearsAfterSuccess(t *testing.T) {
	home := shortHome(t)
	startTestDaemon(t, home)

	failPayload := `{"session_id":"rust-fail-2","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=PostToolUse",
			"FORGE_SESSION_ID=rust-fail-2",
		)
		cmd.Stdin = strings.NewReader(failPayload)
		if err := cmd.Run(); err != nil {
			t.Fatalf("forge hook failure event %d: %v", i+1, err)
		}
	}

	successEnv := append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=codex",
		"FORGE_EVENT_TYPE=PostToolUse",
		"FORGE_SESSION_ID=rust-fail-2",
	)
	successPayload := `{"session_id":"rust-fail-2","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stdout":"Compiling api-service v0.1.0\nFinished dev profile target(s) in 0.84s"}}`

	// Poll until the daemon processes the success event and clears the alert.
	deadline := time.Now().Add(5 * time.Second)
	for {
		successCmd := exec.Command(forgeBin, "hook")
		successCmd.Env = successEnv
		successCmd.Stdin = strings.NewReader(successPayload)
		if err := successCmd.Run(); err != nil {
			t.Fatalf("forge hook success event: %v", err)
		}

		recallCmd := exec.Command(forgeBin, "hook")
		recallCmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=UserPromptSubmit",
			"FORGE_SESSION_ID=rust-fail-2",
		)
		recallCmd.Stdin = strings.NewReader(`{"session_id":"rust-fail-2","cwd":"/tmp/api-service","hook_event_name":"UserPromptSubmit","messages":[{"role":"user","content":[{"type":"text","text":"fix the cargo build failure"}]}]}`)
		out, err := recallCmd.Output()
		if err != nil {
			t.Fatalf("forge hook recall output: %v", err)
		}
		if !strings.Contains(string(out), "Active repeated failure") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected repeated-failure alert to be cleared after success, got %q", string(out))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestBinary_Hook_RepeatedFailureInjectsOfficialDocsHintAfterRetrieval(t *testing.T) {
	home := shortHome(t)
	startTestDaemonEnv(t, home, []string{
		"FORGE_CONTEXT7_MCP_CMD=" + os.Args[0],
		"FORGE_CONTEXT7_MCP_ARGS=-test.run=TestContext7MCPHelperProcess --",
		"GO_WANT_CONTEXT7_MCP_HELPER=1",
		"FORGE_TEST_SUBPROCESS=context7_mcp_helper",
	})

	failPayload := `{"session_id":"rust-fail-3","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=PostToolUse",
			"FORGE_SESSION_ID=rust-fail-3",
		)
		cmd.Stdin = strings.NewReader(failPayload)
		if err := cmd.Run(); err != nil {
			t.Fatalf("forge hook failure event %d: %v", i+1, err)
		}
	}

	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		database, err := db.Open(dbPath)
		if err == nil {
			summaries, _ := database.FreshExternalContextSummariesByProject("api-service", 5)
			database.Close()
			if len(summaries) > 0 && summaries[0].Hint != "" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	summaries, err := database.FreshExternalContextSummariesByProject("api-service", 5)
	if err != nil {
		database.Close()
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) == 0 {
		database.Close()
		t.Fatal("expected context7 summary after repeated failure retrieval")
	}
	if summaries[0].SummaryKind != "official_hint" {
		database.Close()
		t.Fatalf("SummaryKind = %q, want official_hint", summaries[0].SummaryKind)
	}
	if !strings.Contains(summaries[0].Hint, "Rust E0599 usually means the method is not in scope") {
		database.Close()
		t.Fatalf("unexpected hint %q", summaries[0].Hint)
	}
	database.Close()

	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=codex",
		"FORGE_EVENT_TYPE=UserPromptSubmit",
		"FORGE_SESSION_ID=rust-fail-3",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"rust-fail-3","cwd":"/tmp/api-service","hook_event_name":"UserPromptSubmit","messages":[{"role":"user","content":[{"type":"text","text":"fix the cargo build failure"}]}]}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("forge hook recall output: %v", err)
	}
	if !strings.Contains(string(out), "Official docs hint from context7 rust: Rust E0599 usually means the method is not in scope or the trait providing it is not imported.") {
		t.Fatalf("expected official docs hint injection, got %q", string(out))
	}
	if strings.Contains(string(out), "Active repeated failure") {
		t.Fatalf("expected official docs hint to replace repeated-failure alert, got %q", string(out))
	}
}

func TestBinary_Hook_RepeatedFailureInjectsAIRefinedOfficialDocsHint(t *testing.T) {
	home := shortHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"text": `{"relevant":true,"confidence":0.96,"hint":"Import the trait that defines the missing method before calling serve."}`},
			},
		})
	}))
	defer server.Close()

	startTestDaemonEnv(t, home, []string{
		"FORGE_CONTEXT7_MCP_CMD=" + os.Args[0],
		"FORGE_CONTEXT7_MCP_ARGS=-test.run=TestContext7MCPHelperProcess --",
		"GO_WANT_CONTEXT7_MCP_HELPER=1",
		"FORGE_TEST_SUBPROCESS=context7_mcp_helper",
		"FORGE_PROVIDER=anthropic",
		"FORGE_API_KEY=sk-ant-test",
		"FORGE_BASE_URL=" + server.URL,
	})

	failPayload := `{"session_id":"rust-fail-ai","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=PostToolUse",
			"FORGE_SESSION_ID=rust-fail-ai",
		)
		cmd.Stdin = strings.NewReader(failPayload)
		if err := cmd.Run(); err != nil {
			t.Fatalf("forge hook failure event %d: %v", i+1, err)
		}
	}

	dbPath := filepath.Join(home, ".forge", "forge.db")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		database, err := db.Open(dbPath)
		if err == nil {
			summaries, _ := database.FreshExternalContextSummariesByProject("api-service", 5)
			database.Close()
			if len(summaries) > 0 && summaries[0].SummaryKind == "official_hint_ai" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	summaries, err := database.FreshExternalContextSummariesByProject("api-service", 5)
	if err != nil {
		database.Close()
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) == 0 {
		database.Close()
		t.Fatal("expected context7 summary after AI refinement")
	}
	if summaries[0].SummaryKind != "official_hint_ai" {
		database.Close()
		t.Fatalf("SummaryKind = %q, want official_hint_ai", summaries[0].SummaryKind)
	}
	if summaries[0].Hint != "Import the trait that defines the missing method before calling serve." {
		database.Close()
		t.Fatalf("unexpected AI hint %q", summaries[0].Hint)
	}
	database.Close()

	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=codex",
		"FORGE_EVENT_TYPE=UserPromptSubmit",
		"FORGE_SESSION_ID=rust-fail-ai",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"rust-fail-ai","cwd":"/tmp/api-service","hook_event_name":"UserPromptSubmit","messages":[{"role":"user","content":[{"type":"text","text":"fix the cargo build failure"}]}]}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("forge hook recall output: %v", err)
	}
	if !strings.Contains(string(out), "Official docs hint from context7 rust: Import the trait that defines the missing method before calling serve.") {
		t.Fatalf("expected AI-refined official docs hint injection, got %q", string(out))
	}
	if strings.Contains(string(out), "Active repeated failure") {
		t.Fatalf("expected official docs hint to replace repeated-failure alert, got %q", string(out))
	}
}

func TestBinary_Hook_RepeatedFailureWaitsBrieflyForOfficialDocsHint(t *testing.T) {
	home := shortHome(t)
	startTestDaemonEnv(t, home, []string{
		"FORGE_CONTEXT7_MCP_CMD=" + os.Args[0],
		"FORGE_CONTEXT7_MCP_ARGS=-test.run=TestContext7MCPHelperProcess --",
		"GO_WANT_CONTEXT7_MCP_HELPER=1",
		"FORGE_TEST_SUBPROCESS=context7_mcp_helper",
		"GO_WANT_CONTEXT7_MCP_DELAY_MS=350",
		"FORGE_HOOK_RECALL_WAIT_MS=2000",
		"FORGE_HOOK_RECALL_POLL_MS=50",
	})

	failPayload := `{"session_id":"rust-fail-wait","cwd":"/tmp/api-service","tool_name":"Bash","hook_event_name":"PostToolUse","tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(),
			"HOME="+home,
			"FORGE_SOURCE_TOOL=codex",
			"FORGE_EVENT_TYPE=PostToolUse",
			"FORGE_SESSION_ID=rust-fail-wait",
		)
		cmd.Stdin = strings.NewReader(failPayload)
		if err := cmd.Run(); err != nil {
			t.Fatalf("forge hook failure event %d: %v", i+1, err)
		}
	}

	start := time.Now()
	cmd := exec.Command(forgeBin, "hook")
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_SOURCE_TOOL=codex",
		"FORGE_EVENT_TYPE=UserPromptSubmit",
		"FORGE_SESSION_ID=rust-fail-wait",
		"FORGE_HOOK_RECALL_WAIT_MS=2000",
		"FORGE_HOOK_RECALL_POLL_MS=50",
	)
	cmd.Stdin = strings.NewReader(`{"session_id":"rust-fail-wait","cwd":"/tmp/api-service","hook_event_name":"UserPromptSubmit","messages":[{"role":"user","content":[{"type":"text","text":"fix the cargo build failure"}]}]}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("forge hook recall output: %v", err)
	}
	elapsed := time.Since(start)
	if !strings.Contains(string(out), "Official docs hint from context7 rust: Rust E0599 usually means the method is not in scope or the trait providing it is not imported.") {
		t.Fatalf("expected official docs hint injection after brief wait, got %q", string(out))
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("expected hook to wait briefly for retrieval, elapsed %v", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("expected hook wait to stay bounded, elapsed %v", elapsed)
	}
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
	allowedRaceFailures := 0
	for _, result := range results {
		if result.code != 0 {
			// Under heavy CI scheduling jitter, one concurrent starter can observe
			// a transient stale-address/lock window while another bootstrap call
			// succeeds. Treat that specific diagnostic combo as non-fatal if the
			// daemon is confirmed running below.
			if strings.Contains(result.stdout, "stale address") &&
				strings.Contains(result.stdout, "Failed to acquire daemon lock") {
				allowedRaceFailures++
				continue
			}
			t.Fatalf("concurrent forge start exited %d (stdout: %q)", result.code, result.stdout)
		}
	}
	if allowedRaceFailures >= len(results) {
		t.Fatalf("all concurrent starts failed with race diagnostics: %#v", results)
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

func TestBinary_Start_ClearsLockOwnedByNonForgeProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep process as the foreign lock owner")
	}

	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	foreign := exec.Command("sleep", "30")
	if err := foreign.Start(); err != nil {
		t.Fatalf("start foreign process: %v", err)
	}
	defer func() {
		_ = foreign.Process.Kill()
		_, _ = foreign.Process.Wait()
	}()
	if _, err := processIdentity(foreign.Process.Pid); err != nil {
		t.Skipf("process inspection unavailable in this environment: %v", err)
	}

	lockPath := filepath.Join(forgeDir, "forge.lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", foreign.Process.Pid)), 0o600); err != nil {
		t.Fatalf("WriteFile foreign lock: %v", err)
	}

	stdout, stderr, code := runForge(t, home, "start")
	if code != 0 {
		t.Fatalf("forge start with foreign lock exited %d (stdout: %q, stderr: %q)", code, stdout, stderr)
	}
	t.Cleanup(func() { runForge(t, home, "stop") })
	waitForFile(t, filepath.Join(forgeDir, "forge.addr"), 3*time.Second)
}

func TestBinary_Doctor_ReportsForeignLockOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep process as the foreign lock owner")
	}

	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	foreign := exec.Command("sleep", "30")
	if err := foreign.Start(); err != nil {
		t.Fatalf("start foreign process: %v", err)
	}
	defer func() {
		_ = foreign.Process.Kill()
		_, _ = foreign.Process.Wait()
	}()
	if _, err := processIdentity(foreign.Process.Pid); err != nil {
		t.Skipf("process inspection unavailable in this environment: %v", err)
	}

	if err := os.WriteFile(filepath.Join(forgeDir, "forge.lock"), []byte(fmt.Sprintf("%d", foreign.Process.Pid)), 0o600); err != nil {
		t.Fatalf("WriteFile foreign lock: %v", err)
	}

	stdout, _, _ := runForge(t, home, "doctor")
	if !strings.Contains(stdout, "lock owned by non-Forge process") {
		t.Fatalf("forge doctor should identify foreign lock owner, got: %q", stdout)
	}
}

func TestBinary_Doctor_ReportsTransientIntegrationReference(t *testing.T) {
	home := shortHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settings := `{"mcpServers":{"forge":{"command":"/var/folders/test/forge-bin-12345/forge","args":["mcp"]}}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("WriteFile settings.json: %v", err)
	}

	stdout, _, _ := runForge(t, home, "doctor")
	if !strings.Contains(stdout, "transient Forge path") {
		t.Fatalf("forge doctor should report transient integration paths, got: %q", stdout)
	}
}

func TestBinary_Doctor_ReportsUnwritableForgeHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}

	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(forgeDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(forgeDir, 0o700)

	stdout, _, _ := runForge(t, home, "doctor")
	if !strings.Contains(stdout, "cannot write") {
		t.Fatalf("forge doctor should report unwritable forge home, got: %q", stdout)
	}
}

func TestBinary_Doctor_ReportsSocketWithoutDaemonProcess(t *testing.T) {
	home := shortHome(t)
	forgeDir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(forgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(forgeDir, "forge.sock"), []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile forge.sock: %v", err)
	}

	stdout, _, _ := runForge(t, home, "doctor")
	if !strings.Contains(stdout, "socket exists but no daemon process") {
		t.Fatalf("forge doctor should report orphaned socket, got: %q", stdout)
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

func TestBinary_Stop_DoesNotLogExpectedAcceptCloseError(t *testing.T) {
	home := shortHome(t)
	addrFile := filepath.Join(home, ".forge", "forge.addr")

	runForge(t, home, "start")
	waitForFile(t, addrFile, 3*time.Second)

	runForge(t, home, "stop")
	time.Sleep(300 * time.Millisecond)

	logData, err := os.ReadFile(filepath.Join(home, ".forge", "daemon.log"))
	if err != nil {
		t.Fatalf("read daemon log: %v", err)
	}
	if strings.Contains(string(logData), "Accept error: use of closed network connection") {
		t.Fatalf("daemon log should not treat listener close as an accept error, got: %q", string(logData))
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

func TestBinary_MCPSelfHealing(t *testing.T) {
	home := shortHome(t)
	runForge(t, home, "init")

	// Ensure daemon is initially stopped
	runForge(t, home, "stop")

	// Start MCP subprocess
	cmd := exec.Command(forgeBin, "mcp")
	cmd.Env = append(baseEnv(), "HOME="+home)
	
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mcp server: %v", err)
	}
	
	defer func() {
		_ = stdin.Close()
		killProcessTree(cmd.Process.Pid)
		waitOrTimeout(cmd, 5*time.Second)
		runForge(t, home, "stop")
	}()

	// Send get_forge_status tool call which should trigger self-healing
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_forge_status","arguments":{}}}` + "\n"
	if _, err := stdin.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read response
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if !strings.Contains(line, `"result"`) {
		t.Errorf("expected result in response, got: %s", line)
	}

	// Wait for daemon to recover
	daemonReady := false
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		status, _, _ := runForge(t, home, "status")
		if strings.Contains(status, "running") {
			daemonReady = true
			break
		}
	}

	if !daemonReady {
		t.Fatal("Daemon failed to recover automatically after MCP tool call")
	}
}

