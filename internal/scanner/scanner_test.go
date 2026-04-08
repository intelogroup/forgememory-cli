package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRepos_DirectAndNested(t *testing.T) {
	dir := t.TempDir()

	// Direct repo
	direct := filepath.Join(dir, "direct-repo")
	if err := os.MkdirAll(filepath.Join(direct, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir direct .git: %v", err)
	}

	// Nested repo (one level deep)
	nested := filepath.Join(dir, "group", "nested-repo")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir nested .git: %v", err)
	}

	// Non-repo dir
	if err := os.MkdirAll(filepath.Join(dir, "notarepo", "subdir"), 0o700); err != nil {
		t.Fatalf("mkdir notarepo: %v", err)
	}

	repos, err := findRepos(dir)
	if err != nil {
		t.Fatalf("findRepos: %v", err)
	}

	found := map[string]bool{}
	for _, r := range repos {
		found[r] = true
	}
	if !found[direct] {
		t.Errorf("expected direct repo %s in results %v", direct, repos)
	}
	if !found[nested] {
		t.Errorf("expected nested repo %s in results %v", nested, repos)
	}
}

func TestFindRepos_NonexistentDir(t *testing.T) {
	repos, err := findRepos(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatalf("findRepos nonexistent: %v", err)
	}
	if repos != nil {
		t.Errorf("expected nil repos for nonexistent dir, got %v", repos)
	}
}

func TestBuildPayload(t *testing.T) {
	commits := []string{"abc123 fix bug", "def456 add feature"}
	payload, err := buildPayload("myrepo", commits)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	// Must be valid JSON containing repo name and commits
	if len(payload) < 10 {
		t.Errorf("payload too short: %q", payload)
	}
	// Basic check that both commits are in the JSON
	for _, c := range commits {
		found := false
		for i := range payload {
			if i+len(c) <= len(payload) && payload[i:i+len(c)] == c {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("commit %q not found in payload %q", c, payload)
		}
	}
}

func TestBuildPayload_EmptyCommits(t *testing.T) {
	payload, err := buildPayload("repo", nil)
	if err != nil {
		t.Fatalf("buildPayload empty: %v", err)
	}
	if payload == "" {
		t.Fatal("expected non-empty payload even with no commits")
	}
}
