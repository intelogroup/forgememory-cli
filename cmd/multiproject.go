package main

import "github.com/forge/forge/internal/db"

// knownProjects returns every project forge has recorded activity for,
// enumerated from the projects table plus any project_id that only exists
// in session_summaries (repos active before the projects table was wired up,
// or non-git working directories that never resolved a git root).
func knownProjects(database *db.DB) ([]db.Project, error) {
	projects, err := database.AllProjects()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(projects))
	for _, p := range projects {
		seen[p.ID] = true
	}

	ids, err := database.ProjectIDsWithEnoughSessions(1)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !seen[id] {
			projects = append(projects, db.Project{ID: id})
			seen[id] = true
		}
	}
	return projects, nil
}
