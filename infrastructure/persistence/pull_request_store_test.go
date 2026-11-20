package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/search"
	"github.com/helixml/kodit/infrastructure/persistence"
	"github.com/helixml/kodit/internal/testdb"
)

func snippetIDs(results []search.Result) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.SnippetID()
	}
	return ids
}

func TestSQLiteBM25Store_PullRequestTableIsIndependent(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()

	files, err := persistence.NewSQLiteBM25Store(db, zerolog.Nop())
	require.NoError(t, err)
	prs, err := persistence.NewSQLiteBM25StoreForTable(db, persistence.SQLitePullRequestBM25Table, zerolog.Nop())
	require.NoError(t, err)

	require.NoError(t, files.Index(ctx, []search.Document{search.NewDocument("1", "kubernetes controller reconciles pods")}))
	require.NoError(t, prs.Index(ctx, []search.Document{search.NewDocument("2", "kubernetes rationale for the controller")}))

	fileHits, err := files.Find(ctx, search.WithQuery("kubernetes"), repository.WithLimit(10))
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, snippetIDs(fileHits))

	prHits, err := prs.Find(ctx, search.WithQuery("kubernetes"), repository.WithLimit(10))
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, snippetIDs(prHits))

	require.NoError(t, prs.DeleteBy(ctx, search.WithSnippetIDs([]string{"1", "2"})))
	fileHits, err = files.Find(ctx, search.WithQuery("kubernetes"), repository.WithLimit(10))
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, snippetIDs(fileHits), "deleting from the pull request table leaves file documents")

	reopened, err := persistence.NewSQLiteBM25StoreForTable(db, persistence.SQLitePullRequestBM25Table, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, reopened.Index(ctx, []search.Document{search.NewDocument("3", "reopened store keeps row ids unique")}))
	count, err := reopened.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func savedRepository(t *testing.T, store persistence.RepositoryStore, url string) repository.Repository {
	t.Helper()
	repo, err := repository.NewRepository(url)
	require.NoError(t, err)
	saved, err := store.Save(context.Background(), repo)
	require.NoError(t, err)
	return saved
}

func TestPullRequestStore_RoundTrip(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := savedRepository(t, persistence.NewRepositoryStore(db), "https://github.com/acme/app")
	store := persistence.NewPullRequestStore(db)

	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	open := pullrequest.ReconstructPullRequest(0, repo.ID(), 7, 11, "Add cache", "https://github.com/acme/app/pull/7",
		pullrequest.StateOpen, "alice", "", created, created.Add(time.Hour), time.Time{}, created.Add(2*time.Hour))

	saved, err := store.Save(ctx, open)
	require.NoError(t, err)
	require.NotZero(t, saved.ID())

	found, err := store.Find(ctx, repository.WithRepoID(repo.ID()), pullrequest.WithNumber(7))
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "Add cache", found[0].Title())
	assert.True(t, found[0].MergedAt().IsZero())
	assert.True(t, found[0].UpdatedAt().Equal(created.Add(time.Hour)))

	merged := pullrequest.ReconstructPullRequest(saved.ID(), repo.ID(), 7, 11, "Add cache", "https://github.com/acme/app/pull/7",
		pullrequest.StateMerged, "alice", "abc", created, created.Add(3*time.Hour), created.Add(3*time.Hour), created.Add(4*time.Hour))
	_, err = store.Save(ctx, merged)
	require.NoError(t, err)

	byEnrichment, err := store.Find(ctx, pullrequest.WithEnrichmentIDIn([]int64{11}))
	require.NoError(t, err)
	require.Len(t, byEnrichment, 1)
	assert.Equal(t, pullrequest.StateMerged, byEnrichment[0].State())
	assert.Equal(t, "abc", byEnrichment[0].MergeCommitSHA())
	assert.True(t, byEnrichment[0].MergedAt().Equal(created.Add(3*time.Hour)))
}

func TestPullRequestCursorStore_SaveCreatesThenUpdates(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	repo := savedRepository(t, persistence.NewRepositoryStore(db), "https://github.com/acme/app")
	store := persistence.NewPullRequestCursorStore(db)

	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	_, err := store.Save(ctx, pullrequest.NewCursor(repo.ID()).StartedFull(start))
	require.NoError(t, err)

	found, err := store.Find(ctx, repository.WithRepoID(repo.ID()))
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.True(t, found[0].FullStartedAt().Equal(start))
	assert.True(t, found[0].Watermark().IsZero())

	_, err = store.Save(ctx, found[0].Completed(start.Add(-time.Hour)))
	require.NoError(t, err)

	found, err = store.Find(ctx, repository.WithRepoID(repo.ID()))
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.True(t, found[0].Watermark().Equal(start.Add(-time.Hour)))
	assert.True(t, found[0].FullSyncedAt().Equal(start))
	assert.True(t, found[0].FullStartedAt().IsZero())
}
