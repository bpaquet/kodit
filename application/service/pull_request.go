package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/helixml/kodit/domain/enrichment"
	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/search"
	"github.com/helixml/kodit/internal/database"
)

// PullRequests indexes pull requests and searches them. Each pull request is
// an enrichment holding its rendered text, associated with its repository and
// indexed in a BM25 store of its own.
type PullRequests struct {
	pullRequests pullrequest.Store
	enrichments  *Enrichment
	associations enrichment.AssociationStore
	repositories repository.RepositoryStore
	index        search.Store
}

// NewPullRequests creates a PullRequests service.
func NewPullRequests(
	pullRequests pullrequest.Store,
	enrichments *Enrichment,
	associations enrichment.AssociationStore,
	repositories repository.RepositoryStore,
	index search.Store,
) *PullRequests {
	return &PullRequests{
		pullRequests: pullRequests,
		enrichments:  enrichments,
		associations: associations,
		repositories: repositories,
		index:        index,
	}
}

// Save indexes a snapshot of a pull request of repo, replacing the indexed
// text of a pull request already indexed under the same number.
func (s *PullRequests) Save(ctx context.Context, repo repository.Repository, snapshot pullrequest.Snapshot, now time.Time) error {
	content := snapshot.Document(repo.UpstreamURL())

	var id int64
	current, found := enrichment.Enrichment{}, false
	existing, err := s.pullRequests.FindOne(ctx, repository.WithRepoID(repo.ID()), pullrequest.WithNumber(snapshot.Number))
	switch {
	case err == nil:
		id = existing.ID()
		current, err = s.enrichments.Get(ctx, repository.WithID(existing.EnrichmentID()))
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, database.ErrNotFound):
			return fmt.Errorf("get pull request #%d enrichment: %w", snapshot.Number, err)
		}
	case !errors.Is(err, database.ErrNotFound):
		return fmt.Errorf("find pull request #%d: %w", snapshot.Number, err)
	}

	if found {
		saved, err := s.enrichments.Save(ctx, current.WithContent(content))
		if err != nil {
			return fmt.Errorf("save pull request #%d enrichment: %w", snapshot.Number, err)
		}
		if err := s.index.DeleteBy(ctx, search.WithSnippetID(strconv.FormatInt(saved.ID(), 10))); err != nil {
			return fmt.Errorf("remove pull request #%d from index: %w", snapshot.Number, err)
		}
		return s.store(ctx, snapshot, saved, repo.ID(), id, now)
	}

	saved, err := s.enrichments.Save(ctx, enrichment.NewPullRequest(content))
	if err != nil {
		return fmt.Errorf("save pull request #%d enrichment: %w", snapshot.Number, err)
	}
	association := enrichment.RepositoryAssociation(saved.ID(), strconv.FormatInt(repo.ID(), 10))
	if _, err := s.associations.Save(ctx, association); err != nil {
		return s.discard(ctx, saved, fmt.Errorf("save pull request #%d association: %w", snapshot.Number, err))
	}
	if err := s.store(ctx, snapshot, saved, repo.ID(), id, now); err != nil {
		return s.discard(ctx, saved, err)
	}
	return nil
}

func (s *PullRequests) store(ctx context.Context, snapshot pullrequest.Snapshot, text enrichment.Enrichment, repoID, id int64, now time.Time) error {
	document := search.NewDocument(strconv.FormatInt(text.ID(), 10), text.Content())
	if err := s.index.Index(ctx, []search.Document{document}); err != nil {
		return fmt.Errorf("index pull request #%d: %w", snapshot.Number, err)
	}
	if _, err := s.pullRequests.Save(ctx, snapshot.PullRequest(repoID, text.ID(), now).WithID(id)); err != nil {
		return fmt.Errorf("save pull request #%d: %w", snapshot.Number, err)
	}
	return nil
}

// discard deletes a text created for a pull request whose indexing failed,
// so the next pass does not leave a second copy behind.
func (s *PullRequests) discard(ctx context.Context, text enrichment.Enrichment, cause error) error {
	if err := s.enrichments.DeleteBy(ctx, repository.WithID(text.ID())); err != nil {
		return errors.Join(cause, fmt.Errorf("discard pull request text %d: %w", text.ID(), err))
	}
	return cause
}

// Remove drops an indexed pull request and its text.
func (s *PullRequests) Remove(ctx context.Context, pr pullrequest.PullRequest) error {
	if err := s.enrichments.DeleteBy(ctx, repository.WithID(pr.EnrichmentID())); err != nil {
		return fmt.Errorf("delete pull request #%d enrichment: %w", pr.Number(), err)
	}
	if err := s.pullRequests.Delete(ctx, pr); err != nil {
		return fmt.Errorf("delete pull request #%d: %w", pr.Number(), err)
	}
	return nil
}

// Search returns the pull requests best matching query, restricted to
// repoIDs when not empty.
func (s *PullRequests) Search(ctx context.Context, query string, limit int, repoIDs []int64) ([]pullrequest.Hit, error) {
	opts := []repository.Option{search.WithQuery(query), repository.WithLimit(limit)}
	if len(repoIDs) > 0 {
		opts = append(opts, search.WithFilters(search.NewFilters(search.WithSourceRepos(repoIDs))))
	}
	results, err := s.index.Find(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("search pull requests: %w", err)
	}
	if len(results) == 0 {
		return []pullrequest.Hit{}, nil
	}

	ids := make([]int64, 0, len(results))
	for _, r := range results {
		id, err := strconv.ParseInt(r.SnippetID(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse pull request document id %q: %w", r.SnippetID(), err)
		}
		ids = append(ids, id)
	}

	prs, err := s.pullRequests.Find(ctx, pullrequest.WithEnrichmentIDIn(ids))
	if err != nil {
		return nil, fmt.Errorf("find pull requests: %w", err)
	}
	byEnrichment := make(map[int64]pullrequest.PullRequest, len(prs))
	owners := make([]int64, 0, len(prs))
	for _, pr := range prs {
		byEnrichment[pr.EnrichmentID()] = pr
		owners = append(owners, pr.RepoID())
	}

	texts, err := s.texts(ctx, ids)
	if err != nil {
		return nil, err
	}
	urls, err := s.repositoryURLs(ctx, owners)
	if err != nil {
		return nil, err
	}

	hits := make([]pullrequest.Hit, 0, len(results))
	for i, r := range results {
		pr, ok := byEnrichment[ids[i]]
		if !ok {
			continue
		}
		hits = append(hits, pullrequest.NewHit(pr, urls[pr.RepoID()], texts[ids[i]], r.Score()))
	}
	return hits, nil
}

// Get returns the indexed pull request of repoID with the given number.
func (s *PullRequests) Get(ctx context.Context, repoID int64, number int) (pullrequest.Hit, error) {
	pr, err := s.pullRequests.FindOne(ctx, repository.WithRepoID(repoID), pullrequest.WithNumber(number))
	if errors.Is(err, database.ErrNotFound) {
		return pullrequest.Hit{}, fmt.Errorf("pull request #%d: %w", number, ErrPullRequestNotFound)
	}
	if err != nil {
		return pullrequest.Hit{}, fmt.Errorf("find pull request #%d: %w", number, err)
	}

	texts, err := s.texts(ctx, []int64{pr.EnrichmentID()})
	if err != nil {
		return pullrequest.Hit{}, err
	}
	urls, err := s.repositoryURLs(ctx, []int64{pr.RepoID()})
	if err != nil {
		return pullrequest.Hit{}, err
	}
	return pullrequest.NewHit(pr, urls[pr.RepoID()], texts[pr.EnrichmentID()], 0), nil
}

func (s *PullRequests) texts(ctx context.Context, ids []int64) (map[int64]string, error) {
	found, err := s.enrichments.Find(ctx, repository.WithIDIn(ids))
	if err != nil {
		return nil, fmt.Errorf("find pull request texts: %w", err)
	}
	texts := make(map[int64]string, len(found))
	for _, e := range found {
		texts[e.ID()] = e.Content()
	}
	return texts, nil
}

func (s *PullRequests) repositoryURLs(ctx context.Context, ids []int64) (map[int64]string, error) {
	repos, err := s.repositories.Find(ctx, repository.WithIDIn(ids))
	if err != nil {
		return nil, fmt.Errorf("find pull request repositories: %w", err)
	}
	urls := make(map[int64]string, len(repos))
	for _, r := range repos {
		urls[r.ID()] = r.UpstreamURL()
	}
	return urls, nil
}

// Ensure PullRequests implements pullrequest.Searcher.
var _ pullrequest.Searcher = (*PullRequests)(nil)
