package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionFileMatchesGitTag catches the failure mode where a release tag
// (vX.Y.Z) is created but the VERSION file at that commit was not bumped, or
// vice versa. Five seconds of CI catches the kind of confusion that produced
// "v0.4.38 is tagged but VERSION says 0.4.37" reports.
//
// The test only asserts when HEAD is exactly on an annotated tag; on
// development commits between tags it skips. Run from the repo root.
func TestVersionFileMatchesGitTag(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}

	exact, err := exec.Command("git", "-C", repoRoot, "describe", "--tags", "--exact-match", "HEAD").Output()
	if err != nil {
		t.Skipf("HEAD is not on an annotated tag; skipping (this test only runs on tag commits)")
	}
	tag := strings.TrimSpace(string(exact))
	tagVersion := strings.TrimPrefix(tag, "v")

	raw, err := os.ReadFile(filepath.Join(repoRoot, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	fileVersion := strings.TrimSpace(string(raw))

	if fileVersion != tagVersion {
		t.Fatalf("VERSION file = %q but git tag at HEAD = %q — bump VERSION before tagging, or retag with the matching version",
			fileVersion, tag)
	}
}

func findRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
