package pullrequest

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/helixml/kodit/domain/repository"
)

// Store persists indexed pull requests.
type Store interface {
	repository.Store[PullRequest]
}

// CursorStore persists the sync cursor of each repository.
type CursorStore interface {
	repository.Store[Cursor]
}

// Hit is a pull request matched by a search, with its full text and score.
type Hit struct {
	pullRequest PullRequest
	repoURL     string
	document    string
	score       float64
}

// NewHit creates a search Hit.
func NewHit(pullRequest PullRequest, repoURL, document string, score float64) Hit {
	return Hit{pullRequest: pullRequest, repoURL: repoURL, document: document, score: score}
}

func (h Hit) PullRequest() PullRequest { return h.pullRequest }
func (h Hit) RepoURL() string          { return h.repoURL }
func (h Hit) Document() string         { return h.document }
func (h Hit) Score() float64           { return h.score }

// Excerpt returns the document from its description on, at most n bytes,
// cut on a rune boundary.
func (h Hit) Excerpt(n int) string {
	text := h.document
	if i := strings.Index(text, descriptionHeading); i >= 0 {
		text = text[i+len(descriptionHeading):]
	}
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "..."
}

// Searcher finds indexed pull requests.
type Searcher interface {
	Search(ctx context.Context, query string, limit int, repoIDs []int64) ([]Hit, error)
	Get(ctx context.Context, repoID int64, number int) (Hit, error)
}
