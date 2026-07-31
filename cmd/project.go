package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// detectProjectID returns a stable project identifier for the current repo.
// Prefer the git root basename so hooks, manual saves, and MCP retrieval all
// converge on the same project_id instead of mixing absolute paths and names.
func detectProjectID() string {
	return detectProjectIDForPath("")
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
