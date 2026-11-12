package health

import (
	"context"
	"errors"
	"fmt"

	"github.com/jamesread/armature-health/v2/store"
)

// Pushed issues come from the app as things happen (a config value failing to load, a file
// watcher error, a call to a deprecated API) rather than from a Check that recomputes them.
// They are grouped under a source ID, which is stored and listed like a check ID. Name the
// source in Options.SourceTitles to give it a title.
//
// A source ID must not also be a registered check's ID: a check run resolves every issue of
// its ID that it does not return, so it would resolve the pushed ones.

// ErrMissingKey is returned when a pushed issue has no Key.
var ErrMissingKey = errors.New("health issue has no key")

// Raise records one occurrence of a pushed issue. A new issue is stored and notified like a
// check's; raising an open issue's key again adds to its occurrences (issue.Count, or 1),
// moves its last-seen time to now and replaces its severity, title, detail, link and
// attributes; if the severity increased, it is un-acknowledged and notified again. It stays
// open until Resolve or SetIssues resolves it.
func (s *Service) Raise(ctx context.Context, sourceID string, issue Issue) error {
	if err := s.validateSource(sourceID); err != nil {
		return err
	}
	if issue.Key == "" {
		return ErrMissingKey
	}
	s.pushMu.Lock()
	defer s.pushMu.Unlock()

	openByKey, err := s.openIssuesByKey(ctx, sourceID)
	if err != nil {
		return err
	}
	return s.apply(ctx, sourceID, s.CheckTitle(sourceID), planRaise(sourceID, issue, openByKey, s.opts.Now().Unix()))
}

// Resolve resolves a source's open issue by key. It does nothing if there is none.
func (s *Service) Resolve(ctx context.Context, sourceID, key string) error {
	if err := s.validateSource(sourceID); err != nil {
		return err
	}
	s.pushMu.Lock()
	defer s.pushMu.Unlock()

	openByKey, err := s.openIssuesByKey(ctx, sourceID)
	if err != nil {
		return err
	}
	open, ok := openByKey[key]
	if !ok {
		return nil
	}
	return s.apply(ctx, sourceID, "", plan{changes: store.IssueChanges{Resolve: []int64{open.ID}, ResolvedAt: s.opts.Now().Unix()}})
}

// SetIssues replaces a source's open issues with issues, as a check run would: new keys are
// stored and notified, keys still present are refreshed (keeping their first-seen time), and
// missing keys are resolved. Use it for sets recomputed elsewhere, such as the problems found
// when loading configuration. SetIssues(ctx, sourceID, nil) resolves all of a source's issues.
func (s *Service) SetIssues(ctx context.Context, sourceID string, issues []Issue) error {
	if err := s.validateSource(sourceID); err != nil {
		return err
	}
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	return s.reconcile(ctx, sourceID, s.CheckTitle(sourceID), issues)
}

func (s *Service) validateSource(sourceID string) error {
	if sourceID == "" {
		return errors.New("health issue source ID is empty")
	}
	if s.find(sourceID) != nil {
		return fmt.Errorf("%q is a registered health check; push issues under a different source ID", sourceID)
	}
	return nil
}

// planRaise inserts the issue, or adds this occurrence to the open issue with its key.
func planRaise(sourceID string, issue Issue, openByKey map[string]store.Issue, now int64) plan {
	var p plan
	stored := toStored(sourceID, issue, now)
	open, ok := openByKey[issue.Key]
	if !ok {
		p.insert(stored)
		return p
	}
	stored.Occurrences += open.Occurrences
	p.update(open, stored)
	return p
}
