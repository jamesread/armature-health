// Package memstore implements store.Store in memory. Issues and check runs are lost when the
// process exits, so it suits apps without a database, tests, and single-instance deployments
// that only need to show current problems.
package memstore

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/jamesread/armature-health/v2/store"
)

// Memory stores issues and check runs in maps guarded by a mutex. The zero value is not
// usable; call New.
type Memory struct {
	issues map[int64]store.Issue
	runs   map[string]store.CheckRun
	mu     sync.Mutex
	nextID int64
}

var _ store.Store = (*Memory)(nil)

// New returns an empty in-memory store.
func New() *Memory {
	return &Memory{
		issues: map[int64]store.Issue{},
		runs:   map[string]store.CheckRun{},
	}
}

func (m *Memory) ListOpenIssuesForCheck(_ context.Context, checkID string) ([]store.Issue, error) {
	return m.filter(func(i store.Issue) bool { return i.CheckID == checkID && i.ResolvedAt == nil }), nil
}

// ApplyIssueChanges applies all the changes under one lock, so readers never see part of them.
func (m *Memory) ApplyIssueChanges(_ context.Context, changes store.IssueChanges) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, issue := range changes.Insert {
		m.insertLocked(issue)
	}
	for _, issue := range changes.Update {
		m.updateLocked(issue)
	}
	for _, id := range changes.Resolve {
		m.resolveLocked(id, changes.ResolvedAt)
	}
	return nil
}

func (m *Memory) insertLocked(issue store.Issue) {
	m.nextID++
	issue.ID = m.nextID
	issue.ResolvedAt, issue.AcknowledgedAt, issue.AcknowledgedBy = nil, nil, nil
	issue.Attributes = store.CloneAttributes(issue.Attributes)
	m.issues[issue.ID] = issue
}

func (m *Memory) updateLocked(issue store.Issue) {
	stored, ok := m.issues[issue.ID]
	if !ok {
		return
	}
	stored.Severity, stored.Title, stored.Detail, stored.Link = issue.Severity, issue.Title, issue.Detail, issue.Link
	stored.Attributes = store.CloneAttributes(issue.Attributes)
	stored.LastSeen, stored.Occurrences = issue.LastSeen, issue.Occurrences
	m.issues[issue.ID] = stored
}

func (m *Memory) resolveLocked(id int64, at int64) {
	stored, ok := m.issues[id]
	if !ok || stored.ResolvedAt != nil {
		return
	}
	stored.ResolvedAt = &at
	m.issues[id] = stored
}

func (m *Memory) ListIssues(_ context.Context, includeResolved, includeAcknowledged bool) ([]store.Issue, error) {
	issues := m.filter(func(i store.Issue) bool {
		return (includeResolved || i.ResolvedAt == nil) && (includeAcknowledged || i.AcknowledgedAt == nil)
	})
	sort.Slice(issues, func(a, b int) bool {
		if issues[a].LastSeen != issues[b].LastSeen {
			return issues[a].LastSeen > issues[b].LastSeen
		}
		return issues[a].ID > issues[b].ID
	})
	return issues, nil
}

func (m *Memory) SetIssueAcknowledged(_ context.Context, id int64, acknowledged bool, userID string, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.issues[id]
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrIssueNotFound, id)
	}
	stored.AcknowledgedAt, stored.AcknowledgedBy = nil, nil
	if acknowledged {
		stored.AcknowledgedAt, stored.AcknowledgedBy = &at, &userID
	}
	m.issues[id] = stored
	return nil
}

func (m *Memory) Summary(_ context.Context) (store.Summary, error) {
	var summary store.Summary
	for _, issue := range m.filter(func(i store.Issue) bool { return i.ResolvedAt == nil && i.AcknowledgedAt == nil }) {
		switch issue.Severity {
		case "error":
			summary.Errors++
		case "warning":
			summary.Warnings++
		default:
			summary.Infos++
		}
	}
	return summary, nil
}

func (m *Memory) PruneResolvedIssues(_ context.Context, before int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, issue := range m.issues {
		if issue.ResolvedAt != nil && *issue.ResolvedAt < before {
			delete(m.issues, id)
		}
	}
	return nil
}

func (m *Memory) SaveCheckRun(_ context.Context, run store.CheckRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[run.CheckID] = run
	return nil
}

func (m *Memory) ListCheckRuns(_ context.Context) ([]store.CheckRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runs := make([]store.CheckRun, 0, len(m.runs))
	for _, run := range m.runs {
		runs = append(runs, run)
	}
	return runs, nil
}

// filter returns copies of the matching issues, so callers cannot change stored state through
// the optional (pointer) fields or the attributes map.
func (m *Memory) filter(match func(store.Issue) bool) []store.Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Issue
	for _, issue := range m.issues {
		if match(issue) {
			out = append(out, clone(issue))
		}
	}
	return out
}

func clone(issue store.Issue) store.Issue {
	issue.ResolvedAt = clonePtr(issue.ResolvedAt)
	issue.AcknowledgedAt = clonePtr(issue.AcknowledgedAt)
	issue.AcknowledgedBy = clonePtr(issue.AcknowledgedBy)
	issue.Attributes = store.CloneAttributes(issue.Attributes)
	return issue
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
