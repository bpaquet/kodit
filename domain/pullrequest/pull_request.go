// Package pullrequest models pull requests fetched from a code host and
// indexed for search.
package pullrequest

import "time"

// State is the state of a pull request on its code host.
type State string

// Pull request states.
const (
	StateOpen   State = "OPEN"
	StateMerged State = "MERGED"
	StateClosed State = "CLOSED"
)

// Indexed reports whether pull requests in this state are kept in the index.
// Pull requests closed without merging are not.
func (s State) Indexed() bool {
	return s == StateOpen || s == StateMerged
}

// PullRequest is an indexed pull request. Its full text lives in the
// enrichment it points to.
type PullRequest struct {
	id             int64
	repoID         int64
	number         int
	enrichmentID   int64
	title          string
	url            string
	state          State
	author         string
	mergeCommitSHA string
	createdAt      time.Time
	updatedAt      time.Time
	mergedAt       time.Time
	syncedAt       time.Time
}

// ReconstructPullRequest reconstructs a PullRequest from persistence.
func ReconstructPullRequest(
	id, repoID int64,
	number int,
	enrichmentID int64,
	title, url string,
	state State,
	author, mergeCommitSHA string,
	createdAt, updatedAt, mergedAt, syncedAt time.Time,
) PullRequest {
	return PullRequest{
		id:             id,
		repoID:         repoID,
		number:         number,
		enrichmentID:   enrichmentID,
		title:          title,
		url:            url,
		state:          state,
		author:         author,
		mergeCommitSHA: mergeCommitSHA,
		createdAt:      createdAt,
		updatedAt:      updatedAt,
		mergedAt:       mergedAt,
		syncedAt:       syncedAt,
	}
}

func (p PullRequest) ID() int64              { return p.id }
func (p PullRequest) RepoID() int64          { return p.repoID }
func (p PullRequest) Number() int            { return p.number }
func (p PullRequest) EnrichmentID() int64    { return p.enrichmentID }
func (p PullRequest) Title() string          { return p.title }
func (p PullRequest) URL() string            { return p.url }
func (p PullRequest) State() State           { return p.state }
func (p PullRequest) Author() string         { return p.author }
func (p PullRequest) MergeCommitSHA() string { return p.mergeCommitSHA }
func (p PullRequest) CreatedAt() time.Time   { return p.createdAt }
func (p PullRequest) MergedAt() time.Time    { return p.mergedAt }
func (p PullRequest) SyncedAt() time.Time    { return p.syncedAt }

// UpdatedAt returns when the code host last saw activity on the pull request.
func (p PullRequest) UpdatedAt() time.Time { return p.updatedAt }

// WithID returns a copy with the given database ID.
func (p PullRequest) WithID(id int64) PullRequest {
	p.id = id
	return p
}
