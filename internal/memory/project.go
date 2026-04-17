package memory

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ProjectStore is an in-memory implementation of sessions.ProjectStore.
// Projects are never evicted — they persist for the lifetime of the process.
// Safe for concurrent use via a read/write mutex.
type ProjectStore struct {
	mu       sync.RWMutex
	projects map[string]*sessions.Project // keyed by project ID
}

// NewProjectStore returns an empty in-memory ProjectStore.
func NewProjectStore() *ProjectStore {
	return &ProjectStore{projects: make(map[string]*sessions.Project)}
}

// GetProject returns a deep copy of the project with the given ID.
// Returns sessions.ErrProjectNotFound if no such project exists.
func (s *ProjectStore) GetProject(projectID string) (*sessions.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", sessions.ErrProjectNotFound, projectID)
	}
	cp := cloneProject(p)
	return &cp, nil
}

// SaveProject stores a copy of the project, setting UpdatedAt to now.
// Returns an error if ID or UserID is empty.
func (s *ProjectStore) SaveProject(project *sessions.Project) error {
	if project.ID == "" {
		return fmt.Errorf("project must have a non-empty ID")
	}
	if project.UserID == "" {
		return fmt.Errorf("project must have a non-empty UserID")
	}
	cp := cloneProject(project)
	cp.UpdatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[project.ID] = &cp
	return nil
}

// ListProjects returns ProjectSummary entries for all projects owned by userID,
// sorted by UpdatedAt descending (most recently updated first).
func (s *ProjectStore) ListProjects(userID string) ([]*sessions.ProjectSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*sessions.ProjectSummary
	for _, p := range s.projects {
		if p.UserID == userID {
			out = append(out, &sessions.ProjectSummary{
				ID:        p.ID,
				Name:      p.Name,
				Phase:     p.Phase,
				UpdatedAt: p.UpdatedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// cloneProject returns a deep copy to prevent external callers from mutating
// stored state.
func cloneProject(p *sessions.Project) sessions.Project {
	cp := *p
	if p.ADRs != nil {
		cp.ADRs = make([]string, len(p.ADRs))
		copy(cp.ADRs, p.ADRs)
	}
	if p.Milestones != nil {
		cp.Milestones = make([]string, len(p.Milestones))
		copy(cp.Milestones, p.Milestones)
	}
	return cp
}
