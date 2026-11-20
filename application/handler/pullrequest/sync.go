// Package pullrequest provides the task handler that indexes the pull
// requests of a repository.
package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/helixml/kodit/application/handler"
	"github.com/helixml/kodit/application/service"
	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/task"
	"github.com/helixml/kodit/internal/database"
)

const (
	snapshotBatchSize = 5
	// watermarkMargin re-lists pull requests updated just before the
	// watermark, in case GitHub records an update time slightly earlier than
	// the moment the change becomes visible.
	watermarkMargin = 5 * time.Minute
)

// Stores groups the persistence the Sync handler reads and writes.
type Stores struct {
	Repositories repository.RepositoryStore
	PullRequests pullrequest.Store
	Cursors      pullrequest.CursorStore
}

// Sync handles the SYNC_PULL_REQUESTS operation: it lists the pull requests
// of a repository, most recently updated first, and indexes those that are
// new or changed.
//
// An incremental pass stops listing at the watermark of the last completed
// pass. Every fullSyncInterval a full pass lists everything, re-fetches every
// pull request and removes those that are gone. Pull requests closed without
// merging are removed from the index.
type Sync struct {
	stores           Stores
	source           pullrequest.Source
	index            *service.PullRequests
	fullSyncInterval time.Duration
	trackerFactory   handler.TrackerFactory
	logger           zerolog.Logger
}

// NewSync creates a new Sync handler.
func NewSync(
	stores Stores,
	source pullrequest.Source,
	index *service.PullRequests,
	fullSyncInterval time.Duration,
	trackerFactory handler.TrackerFactory,
	logger zerolog.Logger,
) (*Sync, error) {
	if stores.Repositories == nil || stores.PullRequests == nil || stores.Cursors == nil {
		return nil, fmt.Errorf("NewSync: nil store")
	}
	if source == nil {
		return nil, fmt.Errorf("NewSync: nil source")
	}
	if index == nil {
		return nil, fmt.Errorf("NewSync: nil index")
	}
	if fullSyncInterval <= 0 {
		return nil, fmt.Errorf("NewSync: full sync interval must be positive, got %s", fullSyncInterval)
	}
	return &Sync{
		stores:           stores,
		source:           source,
		index:            index,
		fullSyncInterval: fullSyncInterval,
		trackerFactory:   trackerFactory,
		logger:           logger,
	}, nil
}

// Execute processes the SYNC_PULL_REQUESTS task.
func (h *Sync) Execute(ctx context.Context, payload map[string]any) error {
	repoID, err := handler.ExtractInt64(payload, "repository_id")
	if err != nil {
		return err
	}

	tracker := h.trackerFactory.ForOperation(task.OperationSyncPullRequests, payload)

	repo, err := h.stores.Repositories.FindOne(ctx, repository.WithID(repoID))
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}

	cursor, err := h.stores.Cursors.FindOne(ctx, repository.WithRepoID(repoID))
	if errors.Is(err, database.ErrNotFound) {
		cursor, err = pullrequest.NewCursor(repoID), nil
	}
	if err != nil {
		return fmt.Errorf("get pull request cursor: %w", err)
	}

	stored, err := h.stores.PullRequests.Find(ctx, repository.WithRepoID(repoID))
	if err != nil {
		return fmt.Errorf("find pull requests: %w", err)
	}

	now := time.Now()
	full := cursor.Full(now, h.fullSyncInterval)
	if full {
		cursor = cursor.StartedFull(now)
	}
	pass := &syncPass{
		handler: h,
		repo:    repo,
		cursor:  cursor,
		full:    full,
		now:     now,
		stored:  make(map[int]pullrequest.PullRequest, len(stored)),
		seen:    map[int]struct{}{},
		tracker: tracker,
	}
	for _, pr := range stored {
		pass.stored[pr.Number()] = pr
	}

	if err := pass.run(ctx); err != nil {
		if errors.Is(err, pullrequest.ErrUnsupportedRemote) {
			tracker.Skip(ctx, "Repository is not hosted on GitHub")
			return nil
		}
		return err
	}

	h.logger.Info().
		Int64("repo_id", repoID).
		Bool("full", full).
		Int("indexed", pass.indexed).
		Int("removed", pass.removed).
		Msg("pull requests synced")
	return nil
}

type syncPass struct {
	handler *Sync
	repo    repository.Repository
	cursor  pullrequest.Cursor
	full    bool
	now     time.Time
	stored  map[int]pullrequest.PullRequest
	seen    map[int]struct{}
	pending []pullrequest.Summary
	newest  time.Time
	indexed int
	removed int
	tracker handler.Tracker
}

func (p *syncPass) run(ctx context.Context) error {
	if p.full {
		if err := p.list(ctx, time.Time{}); err != nil {
			return err
		}
		// Pull requests updated while the full listing was paged moved ahead
		// of it; list them again so they are not taken for removed ones.
		if err := p.list(ctx, p.cursor.FullStartedAt().Add(-watermarkMargin)); err != nil {
			return err
		}
	} else if err := p.list(ctx, p.cursor.Watermark().Add(-watermarkMargin)); err != nil {
		return err
	}
	if p.full {
		for number, pr := range p.stored {
			if _, ok := p.seen[number]; ok {
				continue
			}
			if err := p.remove(ctx, pr); err != nil {
				return err
			}
		}
	}
	if _, err := p.handler.stores.Cursors.Save(ctx, p.cursor.Completed(p.newest)); err != nil {
		return fmt.Errorf("save pull request cursor: %w", err)
	}
	return nil
}

// list visits pull requests, most recently updated first, until one was
// last updated before stop; a zero stop lists them all.
func (p *syncPass) list(ctx context.Context, stop time.Time) error {
	after := ""
	for {
		page, err := p.handler.source.Summaries(ctx, p.repo.RemoteURL(), after)
		if err != nil {
			return err
		}
		if after == "" {
			if _, err := p.handler.stores.Cursors.Save(ctx, p.cursor); err != nil {
				return fmt.Errorf("save pull request cursor: %w", err)
			}
		}
		for _, summary := range page.Summaries {
			if summary.UpdatedAt.After(p.newest) {
				p.newest = summary.UpdatedAt
			}
			if summary.UpdatedAt.Before(stop) {
				return p.flush(ctx)
			}
			if err := p.visit(ctx, summary); err != nil {
				return err
			}
		}
		if err := p.flush(ctx); err != nil {
			return err
		}
		if !page.HasNext {
			return nil
		}
		if page.Cursor == "" {
			return fmt.Errorf("pull request listing has more pages but no cursor")
		}
		after = page.Cursor
	}
}

func (p *syncPass) visit(ctx context.Context, summary pullrequest.Summary) error {
	p.seen[summary.Number] = struct{}{}
	existing, ok := p.stored[summary.Number]

	if !summary.State.Indexed() {
		if ok {
			return p.remove(ctx, existing)
		}
		return nil
	}

	stale := !ok ||
		!existing.UpdatedAt().Equal(summary.UpdatedAt) ||
		(p.full && existing.SyncedAt().Before(p.cursor.FullStartedAt()))
	if !stale {
		return nil
	}

	p.pending = append(p.pending, summary)
	if len(p.pending) >= snapshotBatchSize {
		return p.flush(ctx)
	}
	return nil
}

func (p *syncPass) flush(ctx context.Context) error {
	if len(p.pending) == 0 {
		return nil
	}
	ids := make([]string, len(p.pending))
	for i, s := range p.pending {
		ids[i] = s.NodeID
	}
	p.pending = nil

	snapshots, err := p.handler.source.Snapshots(ctx, p.repo.RemoteURL(), ids)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if !snapshot.State.Indexed() {
			if existing, ok := p.stored[snapshot.Number]; ok {
				if err := p.remove(ctx, existing); err != nil {
					return err
				}
			}
			continue
		}
		if err := p.handler.index.Save(ctx, p.repo, snapshot, p.now); err != nil {
			return err
		}
		p.indexed++
	}
	p.tracker.SetCurrent(ctx, p.indexed, fmt.Sprintf("Indexed %d pull requests", p.indexed))
	return nil
}

func (p *syncPass) remove(ctx context.Context, pr pullrequest.PullRequest) error {
	if err := p.handler.index.Remove(ctx, pr); err != nil {
		return err
	}
	delete(p.stored, pr.Number())
	p.removed++
	return nil
}

// Ensure Sync implements handler.Handler.
var _ handler.Handler = (*Sync)(nil)
