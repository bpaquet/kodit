package pullrequest

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupportedRemote is returned by a Source for a repository that is not
// hosted on the Source's code host.
var ErrUnsupportedRemote = errors.New("remote is not hosted on a supported code host")

// Summary identifies a pull request and when it last changed, without its
// content.
type Summary struct {
	NodeID    string
	Number    int
	State     State
	UpdatedAt time.Time
}

// SummaryPage is one page of summaries, most recently updated first.
type SummaryPage struct {
	Summaries []Summary
	Cursor    string
	HasNext   bool
}

// Source reads pull requests from a code host. remoteURL is the repository
// remote URL, including any credentials it carries.
type Source interface {
	Summaries(ctx context.Context, remoteURL string, cursor string) (SummaryPage, error)
	Snapshots(ctx context.Context, remoteURL string, nodeIDs []string) ([]Snapshot, error)
}
