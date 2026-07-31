package db

import "database/sql"

// Project is a known repo/project the daemon has seen activity for.
type Project struct {
	ID         string
	GitRoot    string
	Name       string
	LastActive string
	Agents     string
}

// UpsertProject records (or refreshes) a project's git root and last-active
// timestamp, and appends agent to its comma-joined agents list if not
// already present.
func (d *DB) UpsertProject(id, gitRoot, name, agent string) error {
	_, err := d.conn.Exec(
		`INSERT INTO projects(id, git_root, name, last_active, agents)
		 VALUES (?, ?, ?, datetime('now'), ?)
		 ON CONFLICT(id) DO UPDATE SET
		   git_root=excluded.git_root,
		   last_active=excluded.last_active,
		   agents=CASE
		     WHEN agents='' THEN excluded.agents
		     WHEN instr(agents, excluded.agents) > 0 THEN agents
		     ELSE agents || ',' || excluded.agents
		   END`,
		id, gitRoot, name, agent,
	)
	return err
}

// ProjectByID looks up a single project by id.
func (d *DB) ProjectByID(id string) (Project, bool, error) {
	var p Project
	err := d.conn.QueryRow(
		`SELECT id, git_root, name, last_active, agents FROM projects WHERE id=?`, id,
	).Scan(&p.ID, &p.GitRoot, &p.Name, &p.LastActive, &p.Agents)
	if err == sql.ErrNoRows {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, err
	}
	return p, true, nil
}

// AllProjects returns every known project, most recently active first.
func (d *DB) AllProjects() ([]Project, error) {
	rows, err := d.conn.Query(
		`SELECT id, git_root, name, last_active, agents FROM projects ORDER BY last_active DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.GitRoot, &p.Name, &p.LastActive, &p.Agents); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}
