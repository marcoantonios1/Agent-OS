package projectmonitor_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	projectmonitor "github.com/marcoantonios1/Agent-OS/internal/monitors/project"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// mockProjectStore returns scripted projects.
type mockProjectStore struct {
	projects []*sessions.Project
	listErr  error
	getErr   error
}

func (m *mockProjectStore) ListProjects(_ string) ([]*sessions.ProjectSummary, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	summaries := make([]*sessions.ProjectSummary, len(m.projects))
	for i, p := range m.projects {
		summaries[i] = &sessions.ProjectSummary{ID: p.ID, Name: p.Name, Phase: p.Phase, UpdatedAt: p.UpdatedAt}
	}
	return summaries, nil
}
func (m *mockProjectStore) GetProject(id string) (*sessions.Project, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	for _, p := range m.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, sessions.ErrProjectNotFound
}
func (m *mockProjectStore) SaveProject(_ *sessions.Project) error { return nil }

// mockEpisodicProjStore records SaveText calls and returns scripted search results.
type mockEpisodicProjStore struct {
	mu      sync.Mutex
	saved   []episodic.Memory
	results map[string][]episodic.Memory
}

func (m *mockEpisodicProjStore) Save(_ context.Context, _ episodic.Memory, _ []float32) error {
	return nil
}
func (m *mockEpisodicProjStore) SaveText(_ context.Context, mem episodic.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, mem)
	return nil
}
func (m *mockEpisodicProjStore) Search(_ context.Context, _ string, _ []float32, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicProjStore) SearchByText(_ context.Context, _, query string, _ int) ([]episodic.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, mems := range m.results {
		if strings.Contains(query, key) {
			return mems, nil
		}
	}
	return nil, nil
}
func (m *mockEpisodicProjStore) Recent(_ context.Context, _ string, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicProjStore) Delete(_ context.Context, _ string) error { return nil }
func (m *mockEpisodicProjStore) Prune(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockEpisodicProjStore) savedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

// mockProjNotifier records delivered notifications.
type mockProjNotifier struct {
	mu   sync.Mutex
	msgs []*sessions.Reminder
}

func (n *mockProjNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, r)
	return nil
}
func (n *mockProjNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}
func (n *mockProjNotifier) last() *sessions.Reminder {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.msgs) == 0 {
		return nil
	}
	return n.msgs[len(n.msgs)-1]
}

func makeProjectCfg() projectmonitor.Config {
	return projectmonitor.Config{
		Interval:   time.Hour,
		StaleAfter: 3 * 24 * time.Hour,
		Channel:    types.ChannelID("discord"),
		UserID:     "user1",
	}
}

// TestProjectMonitor_StaleProject_NotificationSent: 4-day idle project → notification sent and memory saved.
func TestProjectMonitor_StaleProject_NotificationSent(t *testing.T) {
	staleProject := &sessions.Project{
		ID:        "proj-stale",
		UserID:    "user1",
		Name:      "Agent OS Phase 7",
		Phase:     "tasks",
		UpdatedAt: time.Now().Add(-4 * 24 * time.Hour),
	}

	store := &mockEpisodicProjStore{}
	notifier := &mockProjNotifier{}
	mon := projectmonitor.New(makeProjectCfg(), &mockProjectStore{projects: []*sessions.Project{staleProject}}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 stale notification, got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "Agent OS Phase 7") {
		t.Errorf("notification should name the project, got: %s", msg)
	}
	if !strings.Contains(msg, "4 day") {
		t.Errorf("notification should mention idle days, got: %s", msg)
	}
	if !strings.Contains(msg, "continue Agent OS Phase 7") {
		t.Errorf("notification should include continue prompt, got: %s", msg)
	}
	if store.savedCount() == 0 {
		t.Error("expected episodic memory saved to prevent re-notification")
	}
}

// TestProjectMonitor_RecentProject_NoNotification: project updated 1 hour ago → no notification.
func TestProjectMonitor_RecentProject_NoNotification(t *testing.T) {
	freshProject := &sessions.Project{
		ID:        "proj-fresh",
		UserID:    "user1",
		Name:      "Fresh Project",
		Phase:     "codegen",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}

	store := &mockEpisodicProjStore{}
	notifier := &mockProjNotifier{}
	mon := projectmonitor.New(makeProjectCfg(), &mockProjectStore{projects: []*sessions.Project{freshProject}}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no notification for fresh project, got %d", notifier.count())
	}
}

// TestProjectMonitor_DeadlineApproaching_AlertSent: milestone in 36h → deadline alert sent.
func TestProjectMonitor_DeadlineApproaching_AlertSent(t *testing.T) {
	deadline := time.Now().Add(36 * time.Hour)
	milestoneStr := fmt.Sprintf("MVP Launch — %s", deadline.Format("2006-01-02"))

	project := &sessions.Project{
		ID:         "proj-deadline",
		UserID:     "user1",
		Name:       "Launch Project",
		Phase:      "codegen",
		Milestones: []string{milestoneStr},
		UpdatedAt:  time.Now().Add(-30 * time.Minute),
	}

	store := &mockEpisodicProjStore{}
	notifier := &mockProjNotifier{}
	mon := projectmonitor.New(makeProjectCfg(), &mockProjectStore{projects: []*sessions.Project{project}}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 deadline alert, got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "MVP Launch") {
		t.Errorf("alert should name the milestone, got: %s", msg)
	}
	if !strings.Contains(msg, "Launch Project") {
		t.Errorf("alert should name the project, got: %s", msg)
	}
}

// TestProjectMonitor_AlreadyNotified_NoRepeat: stale project with prior memory → no repeat notification.
func TestProjectMonitor_AlreadyNotified_NoRepeat(t *testing.T) {
	staleProject := &sessions.Project{
		ID:        "proj-repeat",
		UserID:    "user1",
		Name:      "Old Project",
		Phase:     "spec",
		UpdatedAt: time.Now().Add(-5 * 24 * time.Hour),
	}

	store := &mockEpisodicProjStore{
		results: map[string][]episodic.Memory{
			"proj-repeat": {
				{
					ID:        "mem-stale",
					Content:   "Sent stale notification for project proj-repeat (Old Project), idle 5 days",
					Distance:  0.05,
					CreatedAt: time.Now().Add(-2 * time.Hour),
				},
			},
		},
	}
	notifier := &mockProjNotifier{}
	mon := projectmonitor.New(makeProjectCfg(), &mockProjectStore{projects: []*sessions.Project{staleProject}}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no repeat notification for already-notified project, got %d", notifier.count())
	}
}

// TestProjectMonitor_StoreError_NoCrash: project store error → no crash, no notification.
func TestProjectMonitor_StoreError_NoCrash(t *testing.T) {
	store := &mockEpisodicProjStore{}
	notifier := &mockProjNotifier{}
	projectStore := &mockProjectStore{listErr: fmt.Errorf("database locked")}
	mon := projectmonitor.New(makeProjectCfg(), projectStore, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no notifications on store error, got %d", notifier.count())
	}
}
