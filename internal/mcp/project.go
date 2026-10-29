package mcp

import (
	"sync"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/git"
)

var (
	detectedKeyOnce  sync.Once
	detectedKeyValue string
)

// detectedProjectKey resolves the canonical key for the server's working
// directory. Empty when the directory cannot be resolved, in which case
// callers fall back to project_id matching.
func detectedProjectKey() string {
	detectedKeyOnce.Do(func() {
		detectedKeyValue = db.ProjectKey(git.Root(""))
	})
	return detectedKeyValue
}

// projectRefFromArgs resolves the project identity for a tool call. An explicit
// project_id has no filesystem root attached, so it carries no key and relies on
// the legacy project_id fallback.
func projectRefFromArgs(args map[string]any) (id, key string) {
	if raw := stringFromArgs(args, "project_id"); raw != "" {
		if cleaned := cleanProjectID(raw); cleaned != "" {
			return cleaned, ""
		}
	}
	return detectProject(), detectedProjectKey()
}
