package service

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/domain/enrichment"
	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/search"
	"github.com/helixml/kodit/infrastructure/persistence"
	"github.com/helixml/kodit/internal/testdb"
)

type pullRequestFixture struct {
	service     *PullRequests
	enrichments *Enrichment
	files       search.Store
	index       search.Store
	prs         persistence.PullRequestStore
	repoA       repository.Repository
	repoB       repository.Repository
}

func newPullRequestFixture(t *testing.T) pullRequestFixture {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()

	repos := persistence.NewRepositoryStore(db)
	save := func(url string) repository.Repository {
		r, err := repository.NewRepository(url)
		require.NoError(t, err)
		r, err = repos.Save(ctx, r)
		require.NoError(t, err)
		return r
	}

	files, err := persistence.NewSQLiteBM25Store(db, zerolog.Nop())
	require.NoError(t, err)
	index, err := persistence.NewSQLiteBM25StoreForTable(db, persistence.SQLitePullRequestBM25Table, zerolog.Nop())
	require.NoError(t, err)
	associations := persistence.NewAssociationStore(db)
	enrichments := NewEnrichment(persistence.NewEnrichmentStore(db), associations, files, nil, nil, nil, nil, index)
	prs := persistence.NewPullRequestStore(db)

	return pullRequestFixture{
		service:     NewPullRequests(prs, enrichments, associations, repos, index),
		enrichments: enrichments,
		files:       files,
		index:       index,
		prs:         prs,
		repoA:       save("https://github.com/acme/a"),
		repoB:       save("https://github.com/acme/b"),
	}
}

func snapshot(number int, body string) pullrequest.Snapshot {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return pullrequest.Snapshot{
		NodeID: "PR", Number: number, Title: "Change", Body: body, State: pullrequest.StateMerged,
		Author: "alice", CreatedAt: at, UpdatedAt: at, MergedAt: at,
	}
}

func hitNumbers(hits []pullrequest.Hit) []int {
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.PullRequest().Number()
	}
	sort.Ints(out)
	return out
}

func TestPullRequests_SearchFiltersByRepository(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "retry budget rationale"), now))
	require.NoError(t, f.service.Save(ctx, f.repoB, snapshot(2, "retry budget for workers"), now))

	all, err := f.service.Search(ctx, "retry", 10, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, hitNumbers(all))

	onlyA, err := f.service.Search(ctx, "retry", 10, []int64{f.repoA.ID()})
	require.NoError(t, err)
	require.Equal(t, []int{1}, hitNumbers(onlyA))
	assert.Equal(t, "https://github.com/acme/a", onlyA[0].RepoURL())
	assert.Contains(t, onlyA[0].Document(), "retry budget rationale")
	assert.Positive(t, onlyA[0].Score())

	limited, err := f.service.Search(ctx, "retry", 1, nil)
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestPullRequests_SaveReplacesText(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "first draft wording"), time.Now()))
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "final wording"), time.Now()))

	old, err := f.service.Search(ctx, "draft", 10, nil)
	require.NoError(t, err)
	assert.Empty(t, old)
	current, err := f.service.Search(ctx, "wording", 10, nil)
	require.NoError(t, err)
	assert.Len(t, current, 1, "the previous index entry is replaced, not duplicated")

	hit, err := f.service.Get(ctx, f.repoA.ID(), 1)
	require.NoError(t, err)
	assert.Contains(t, hit.Document(), "final wording")

	count, err := f.prs.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	enrichments, err := f.enrichments.Collection.Count(ctx, enrichment.WithSubtype(enrichment.SubtypePullRequest))
	require.NoError(t, err)
	assert.Equal(t, int64(1), enrichments, "an update reuses the enrichment")
}

func TestPullRequests_DoNotReachFileSearch(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "zebra rationale"), time.Now()))

	fileHits, err := f.files.Find(ctx, search.WithQuery("zebra"), repository.WithLimit(10))
	require.NoError(t, err)
	assert.Empty(t, fileHits)
}

func TestPullRequests_Remove(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "obsolete idea"), time.Now()))
	hit, err := f.service.Get(ctx, f.repoA.ID(), 1)
	require.NoError(t, err)

	require.NoError(t, f.service.Remove(ctx, hit.PullRequest()))

	hits, err := f.service.Search(ctx, "obsolete", 10, nil)
	require.NoError(t, err)
	assert.Empty(t, hits)
	_, err = f.service.Get(ctx, f.repoA.ID(), 1)
	assert.ErrorIs(t, err, ErrPullRequestNotFound)
	indexed, err := f.index.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, indexed)
}

func TestPullRequests_ReleasedWithRepository(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "deleted with the repository"), time.Now()))
	require.NoError(t, f.service.Save(ctx, f.repoB, snapshot(2, "kept with the other repository"), time.Now()))

	ids, err := f.enrichments.RepositoryEnrichmentIDs(ctx, f.repoA.ID())
	require.NoError(t, err)
	_, err = f.enrichments.ReleaseForRepository(ctx, ids, f.repoA.ID())
	require.NoError(t, err)

	hits, err := f.service.Search(ctx, "repository", 10, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{2}, hitNumbers(hits))
	indexed, err := f.index.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), indexed, "the pull request index entry of the deleted repository is removed")
}

func TestPullRequests_SaveRecreatesDeletedText(t *testing.T) {
	f := newPullRequestFixture(t)
	ctx := context.Background()
	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "first version"), time.Now()))
	hit, err := f.service.Get(ctx, f.repoA.ID(), 1)
	require.NoError(t, err)
	require.NoError(t, f.enrichments.DeleteBy(ctx, repository.WithID(hit.PullRequest().EnrichmentID())))

	require.NoError(t, f.service.Save(ctx, f.repoA, snapshot(1, "second version"), time.Now()))

	hits, err := f.service.Search(ctx, "version", 10, nil)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Contains(t, hits[0].Document(), "second version")
	count, err := f.prs.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
