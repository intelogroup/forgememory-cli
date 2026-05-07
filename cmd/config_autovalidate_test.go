package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// resolveGoEnv reads GOCACHE/GOMODCACHE/GOPATH from `go env` so the subprocess
// test can pin them to their real locations. Without this, HOME=t.TempDir()
// causes Go's runtime to populate read-only module cache files inside the
// tempdir, which breaks t.TempDir's cleanup.
func resolveGoEnv(t *testing.T) (gocache, gomodcache, gopath string) {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(parts) != 3 {
		t.Fatalf("go env returned %d lines, want 3: %q", len(parts), out)
	}
	return parts[0], parts[1], parts[2]
}

// Regression for the v0.4.38 auto-validate fix: when --provider/--api-key
// changes and --no-validate is not set, `forge config` must hit the provider
// to validate credentials and refuse to save on failure. Previously a stale
// key was silently accepted and the daemon then failed every minute with
// opaque "Invalid API key" errors.
//
// Because runConfig calls os.Exit on validation failure, we use the standard
// Go subprocess pattern (fork the test binary) to capture the exit code.

func TestRunConfig_AutoValidatesAndRejectsBadKey(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if os.Getenv("FORGE_AUTOVALIDATE_CHILD") == "1" {
		runConfig([]string{
			"--provider", "openai",
			"--api-key", "sk-bad",
			"--base-url", os.Getenv("FORGE_TEST_BASE_URL"),
		})
		return
	}

	gocache, gomodcache, gopath := resolveGoEnv(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunConfig_AutoValidatesAndRejectsBadKey$")
	cmd.Env = append(os.Environ(),
		"FORGE_AUTOVALIDATE_CHILD=1",
		"FORGE_TEST_BASE_URL="+srv.URL,
		"HOME="+t.TempDir(),
		"GOCACHE="+gocache,
		"GOMODCACHE="+gomodcache,
		"GOPATH="+gopath,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit, got success. output:\n%s", out)
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("expected ExitError with non-zero code, got %v", err)
	}
	if !strings.Contains(string(out), "Validation failed") {
		t.Fatalf("expected 'Validation failed' on output, got:\n%s", out)
	}
	if !strings.Contains(string(out), "Not saving") {
		t.Fatalf("expected 'Not saving' on output, got:\n%s", out)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("provider was never contacted — auto-validate did not fire")
	}
}

// Companion test: --no-validate must skip the network call entirely AND
// allow the bad key to save. Confirms the opt-out path still works for
// offline scripts.
func TestRunConfig_NoValidateSkipsNetwork(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir())
	runConfig([]string{
		"--provider", "openai",
		"--api-key", "sk-bad",
		"--base-url", srv.URL,
		"--no-validate",
	})

	if h := atomic.LoadInt32(&hits); h != 0 {
		t.Fatalf("--no-validate should not contact provider; got %d hits", h)
	}
}
