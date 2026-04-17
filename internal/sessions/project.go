package sessions

import (
	"errors"
	"time"
)

// ErrProjectNotFound is returned by ProjectStore.GetProject when no project
// exists for the given ID. Callers can check with errors.Is.
var ErrProjectNotFound = errors.New("project not found")

// Project stores the persistent state of a Builder Agent project. It is keyed
// by ID and outlives any single session — a user can return days later and
// resume exactly where they left off.
type Project struct {
	ID         string
	UserID     string
	Name       string
	Phase      string   // requirements | spec | tasks | codegen | review
	Spec       string   // markdown spec produced in spec phase
	Tasks      string   // JSON task list produced in tasks phase
	ActiveTask string   // index of the task currently being worked on
	ADRs       []string // architecture decision records
	Milestones []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ProjectSummary is a lightweight view of a Project returned by ListProjects.
type ProjectSummary struct {
	ID        string
	Name      string
	Phase     string
	UpdatedAt time.Time
}

// ProjectStore persists Builder Agent project state across session boundaries.
// Implementations must be safe for concurrent use.
type ProjectStore interface {
	// GetProject returns the project with the given ID.
	// Returns ErrProjectNotFound if no such project exists.
	GetProject(projectID string) (*Project, error)

	// SaveProject creates or replaces a project. ID and UserID must be non-empty.
	// Implementations set UpdatedAt automatically.
	SaveProject(project *Project) error

	// ListProjects returns lightweight summaries of all projects owned by userID,
	// ordered by UpdatedAt descending (most recently updated first).
	ListProjects(userID string) ([]*ProjectSummary, error)
}
