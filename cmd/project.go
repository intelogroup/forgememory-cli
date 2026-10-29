package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/git"
)

// detectProjectID returns a stable project identifier for the current repo.
// Prefer the git root basename so hooks, manual saves, and MCP retrieval all
// converge on the same project_id instead of mixing absolute paths and names.
func detectProjectID() string {
	return detectProjectIDForPath("")
}

var (
	projectKeyOnce  sync.Once
	projectKeyValue string
)

// currentProjectKey returns the canonical key for the process working
// directory, cached per process. Empty when the directory cannot be resolved,
// in which case callers fall back to project_id matching.
func currentProjectKey() string {
	projectKeyOnce.Do(func() {
		projectKeyValue = db.ProjectKey(git.Root(""))
	})
	return projectKeyValue
}

func detectProjectIDForPath(cwd string) string {
	id, _ := resolveProjectRoot(cwd)
	return id
}

// resolveProjectRoot returns both the project id (git root basename, or cwd
// basename if not a git repo) and the resolved root path itself, so callers
// that need to persist a path (e.g. the projects table) don't have to shell
// out to git a second time.
func resolveProjectRoot(cwd string) (id, gitRoot string) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd == "" {
		return "", ""
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			return filepath.Base(root), root
		}
	}
	return filepath.Base(cwd), cwd
}

func gitState(gitRoot string) (branch, commit string) {
	if gitRoot == "" {
		return "", ""
	}
	out, err := exec.Command("git", "-C", gitRoot, "rev-parse", "--abbrev-ref", "HEAD", "HEAD").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		branch = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		commit = strings.TrimSpace(lines[1])
	}
	return branch, commit
}
