package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Toplevel returns the canonical git top-level directory for path, or "" when
// path is not inside a repository. It resolves symlinks so the same checkout
// reached two ways yields one identity.
func Toplevel(path string) string {
	if strings.TrimSpace(path) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		path = cwd
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

// Root returns the git top-level for path, falling back to the canonical
// absolute path when path is not a repository.
func Root(path string) string {
	if top := Toplevel(path); top != "" {
		return top
	}
	if strings.TrimSpace(path) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		path = cwd
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs
}
