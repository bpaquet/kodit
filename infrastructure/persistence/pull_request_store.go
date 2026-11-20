package persistence

import (
	"time"

	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/internal/database"
)

// PullRequestStore implements pullrequest.Store using GORM.
type PullRequestStore struct {
	database.Repository[pullrequest.PullRequest, PullRequestModel]
}

// NewPullRequestStore creates a new PullRequestStore.
func NewPullRequestStore(db database.Database) PullRequestStore {
	return PullRequestStore{
		Repository: database.NewRepository[pullrequest.PullRequest, PullRequestModel](db, PullRequestMapper{}, "pull request"),
	}
}

// PullRequestMapper maps between pullrequest.PullRequest and PullRequestModel.
type PullRequestMapper struct{}

// ToDomain converts a PullRequestModel to a domain PullRequest.
func (PullRequestMapper) ToDomain(e PullRequestModel) pullrequest.PullRequest {
	return pullrequest.ReconstructPullRequest(
		e.ID, e.RepoID, e.Number, e.EnrichmentID,
		e.Title, e.URL, pullrequest.State(e.State), e.Author, e.MergeCommitSHA,
		e.OpenedAt, e.HostUpdatedAt, timeValue(e.MergedAt), e.SyncedAt,
	)
}

// ToModel converts a domain PullRequest to a PullRequestModel.
func (PullRequestMapper) ToModel(p pullrequest.PullRequest) PullRequestModel {
	return PullRequestModel{
		ID:             p.ID(),
		RepoID:         p.RepoID(),
		Number:         p.Number(),
		EnrichmentID:   p.EnrichmentID(),
		Title:          p.Title(),
		URL:            p.URL(),
		State:          string(p.State()),
		Author:         p.Author(),
		MergeCommitSHA: p.MergeCommitSHA(),
		OpenedAt:       p.CreatedAt(),
		HostUpdatedAt:  p.UpdatedAt(),
		MergedAt:       timePointer(p.MergedAt()),
		SyncedAt:       p.SyncedAt(),
	}
}

// PullRequestCursorStore implements pullrequest.CursorStore using GORM.
type PullRequestCursorStore struct {
	database.Repository[pullrequest.Cursor, PullRequestCursorModel]
}

// NewPullRequestCursorStore creates a new PullRequestCursorStore.
func NewPullRequestCursorStore(db database.Database) PullRequestCursorStore {
	return PullRequestCursorStore{
		Repository: database.NewRepository[pullrequest.Cursor, PullRequestCursorModel](db, PullRequestCursorMapper{}, "pull request cursor"),
	}
}

// PullRequestCursorMapper maps between pullrequest.Cursor and PullRequestCursorModel.
type PullRequestCursorMapper struct{}

// ToDomain converts a PullRequestCursorModel to a domain Cursor.
func (PullRequestCursorMapper) ToDomain(e PullRequestCursorModel) pullrequest.Cursor {
	return pullrequest.ReconstructCursor(e.RepoID, timeValue(e.Watermark), timeValue(e.FullSyncedAt), timeValue(e.FullStartedAt))
}

// ToModel converts a domain Cursor to a PullRequestCursorModel.
func (PullRequestCursorMapper) ToModel(c pullrequest.Cursor) PullRequestCursorModel {
	return PullRequestCursorModel{
		RepoID:        c.RepoID(),
		Watermark:     timePointer(c.Watermark()),
		FullSyncedAt:  timePointer(c.FullSyncedAt()),
		FullStartedAt: timePointer(c.FullStartedAt()),
	}
}

func timeValue(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
