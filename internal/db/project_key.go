package db

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// ProjectKey hashes a canonical project root into a short, stable identifier.
// Project IDs are display names (a git-root basename), so two repositories
// named "api" would otherwise share one memory namespace; the key keeps their
// project-scoped rows distinct. An empty root yields "" (unknown/legacy).
func ProjectKey(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:6])
}
