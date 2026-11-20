package pullrequest

import "time"

// Cursor records how far pull request syncing of a repository has got.
//
// The watermark is the update time of the most recently updated pull request
// seen by the last completed pass: an incremental pass stops listing once it
// reaches older pull requests. A full pass re-fetches every pull request to
// pick up edits that do not move the update time, and removes those that are
// gone. fullStartedAt is set while a full pass is in progress so an
// interrupted pass resumes instead of starting over.
type Cursor struct {
	repoID        int64
	watermark     time.Time
	fullSyncedAt  time.Time
	fullStartedAt time.Time
}

// NewCursor creates the cursor of a repository that was never synced.
func NewCursor(repoID int64) Cursor {
	return Cursor{repoID: repoID}
}

// ReconstructCursor reconstructs a Cursor from persistence.
func ReconstructCursor(repoID int64, watermark, fullSyncedAt, fullStartedAt time.Time) Cursor {
	return Cursor{
		repoID:        repoID,
		watermark:     watermark,
		fullSyncedAt:  fullSyncedAt,
		fullStartedAt: fullStartedAt,
	}
}

func (c Cursor) RepoID() int64            { return c.repoID }
func (c Cursor) Watermark() time.Time     { return c.watermark }
func (c Cursor) FullSyncedAt() time.Time  { return c.fullSyncedAt }
func (c Cursor) FullStartedAt() time.Time { return c.fullStartedAt }

// Full reports whether the next pass at now must be a full pass: one is in
// progress, none ever completed, or the last one is older than interval.
func (c Cursor) Full(now time.Time, interval time.Duration) bool {
	return !c.fullStartedAt.IsZero() || c.fullSyncedAt.IsZero() || now.Sub(c.fullSyncedAt) >= interval
}

// StartedFull returns the cursor once a full pass has started at now,
// keeping the start of a full pass already in progress.
func (c Cursor) StartedFull(now time.Time) Cursor {
	if c.fullStartedAt.IsZero() {
		c.fullStartedAt = now
	}
	return c
}

// Completed returns the cursor once a pass has listed every pull request it
// needed. newest is the update time of the most recently updated pull
// request listed; zero keeps the current watermark.
func (c Cursor) Completed(newest time.Time) Cursor {
	if newest.After(c.watermark) {
		c.watermark = newest
	}
	if !c.fullStartedAt.IsZero() {
		c.fullSyncedAt = c.fullStartedAt
		c.fullStartedAt = time.Time{}
	}
	return c
}
