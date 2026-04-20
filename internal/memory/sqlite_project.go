package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// SQLiteProjectStore implements sessions.ProjectStore backed by a SQLite database.
// Safe for concurrent use — database/sql manages the connection pool.
type SQLiteProjectStore struct {
	db *sql.DB
}

// NewSQLiteProjectStore returns a SQLiteProjectStore using the provided *sql.DB.
// The database must already have the projects table (see RunMigrations).
func NewSQLiteProjectStore(db *sql.DB) *SQLiteProjectStore {
	return &SQLiteProjectStore{db: db}
}

// GetProject returns the project with the given ID.
// Returns sessions.ErrProjectNotFound if no such project exists.
func (s *SQLiteProjectStore) GetProject(projectID string) (*sessions.Project, error) {
	row := s.db.QueryRow(`
		SELECT project_id, user_id, name, phase, spec, tasks, active_task,
		       adrs, milestones, created_at, updated_at
		FROM projects WHERE project_id = ?`, projectID)

	var (
		id, userID, name, phase, spec, tasks, activeTask string
		adrsJSON, milestonesJSON                         string
		createdAt, updatedAt                             time.Time
	)
	err := row.Scan(&id, &userID, &name, &phase, &spec, &tasks, &activeTask,
		&adrsJSON, &milestonesJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", sessions.ErrProjectNotFound, projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("get project %s: %w", projectID, err)
	}

	p := &sessions.Project{
		ID:         id,
		UserID:     userID,
		Name:       name,
		Phase:      phase,
		Spec:       spec,
		Tasks:      tasks,
		ActiveTask: activeTask,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}
	if adrsJSON != "" && adrsJSON != "[]" {
		if err := json.Unmarshal([]byte(adrsJSON), &p.ADRs); err != nil {
			return nil, fmt.Errorf("decode adrs for %s: %w", projectID, err)
		}
	}
	if milestonesJSON != "" && milestonesJSON != "[]" {
		if err := json.Unmarshal([]byte(milestonesJSON), &p.Milestones); err != nil {
			return nil, fmt.Errorf("decode milestones for %s: %w", projectID, err)
		}
	}
	return p, nil
}

// SaveProject creates or replaces a project, setting UpdatedAt to now.
// Returns an error if ID or UserID is empty.
func (s *SQLiteProjectStore) SaveProject(project *sessions.Project) error {
	if project.ID == "" {
		return fmt.Errorf("project must have a non-empty ID")
	}
	if project.UserID == "" {
		return fmt.Errorf("project must have a non-empty UserID")
	}

	adrsJSON, err := json.Marshal(project.ADRs)
	if err != nil {
		return fmt.Errorf("encode adrs: %w", err)
	}
	milestonesJSON, err := json.Marshal(project.Milestones)
	if err != nil {
		return fmt.Errorf("encode milestones: %w", err)
	}

	now := time.Now().UTC()
	createdAt := project.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	_, err = s.db.Exec(`
		INSERT INTO projects
			(project_id, user_id, name, phase, spec, tasks, active_task,
			 adrs, milestones, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			user_id     = excluded.user_id,
			name        = excluded.name,
			phase       = excluded.phase,
			spec        = excluded.spec,
			tasks       = excluded.tasks,
			active_task = excluded.active_task,
			adrs        = excluded.adrs,
			milestones  = excluded.milestones,
			updated_at  = excluded.updated_at`,
		project.ID, project.UserID, project.Name,
		project.Phase, project.Spec, project.Tasks, project.ActiveTask,
		string(adrsJSON), string(milestonesJSON),
		createdAt.UTC(), now,
	)
	if err != nil {
		return fmt.Errorf("save project %s: %w", project.ID, err)
	}
	return nil
}

// ListProjects returns summaries of all projects owned by userID,
// ordered by updated_at descending.
func (s *SQLiteProjectStore) ListProjects(userID string) ([]*sessions.ProjectSummary, error) {
	rows, err := s.db.Query(`
		SELECT project_id, name, phase, updated_at
		FROM projects
		WHERE user_id = ?
		ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects for %s: %w", userID, err)
	}
	defer rows.Close()

	var out []*sessions.ProjectSummary
	for rows.Next() {
		var s sessions.ProjectSummary
		if err := rows.Scan(&s.ID, &s.Name, &s.Phase, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project row: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// compile-time interface check
var _ sessions.ProjectStore = (*SQLiteProjectStore)(nil)
