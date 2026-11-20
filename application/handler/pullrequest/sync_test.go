package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/application/handler"
	"github.com/helixml/kodit/application/service"
	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/task"
	"github.com/helixml/kodit/infrastructure/persistence"
	"github.com/helixml/kodit/internal/testdb"
)

var errRateLimited = errors.New("rate limited")

// fakeSource serves pull requests of one repository, two per page, most
// recently updated first.
type fakeSource struct {
	snapshots   map[int]pullrequest.Snapshot
	listed      map[int]pullrequest.State
	fetched     []int
	failOnPage  int
	pagesServed int
	beforePage  func(page int)
	err         error
}

func newFakeSource() *fakeSource {
	return &fakeSource{snapshots: map[int]pullrequest.Snapshot{}}
}

func (f *fakeSource) put(number int, state pullrequest.State, updated time.Time, body string) {
	f.snapshots[number] = pullrequest.Snapshot{
		NodeID:    fmt.Sprintf("PR_%d", number),
		Number:    number,
		Title:     fmt.Sprintf("Pull request %d", number),
		Body:      body,
		URL:       fmt.Sprintf("https://github.com/acme/app/pull/%d", number),
		State:     state,
		Author:    "alice",
		BaseRef:   "main",
		HeadRef:   fmt.Sprintf("feature-%d", number),
		CreatedAt: updated.Add(-time.Hour),
		UpdatedAt: updated,
	}
}

func (f *fakeSource) Summaries(_ context.Context, _ string, cursor string) (pullrequest.SummaryPage, error) {
	if f.err != nil {
		return pullrequest.SummaryPage{}, f.err
	}
	f.pagesServed++
	if f.failOnPage > 0 && f.pagesServed == f.failOnPage {
		return pullrequest.SummaryPage{}, errRateLimited
	}
	if f.beforePage != nil {
		f.beforePage(f.pagesServed)
	}

	all := make([]pullrequest.Snapshot, 0, len(f.snapshots))
	for _, s := range f.snapshots {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })

	start := 0
	if cursor != "" {
		_, _ = fmt.Sscan(cursor, &start)
	}
	end := min(start+2, len(all))
	page := pullrequest.SummaryPage{HasNext: end < len(all), Cursor: fmt.Sprint(end)}
	for _, s := range all[start:end] {
		state := s.State
		if listed, ok := f.listed[s.Number]; ok {
			state = listed
		}
		page.Summaries = append(page.Summaries, pullrequest.Summary{NodeID: s.NodeID, Number: s.Number, State: state, UpdatedAt: s.UpdatedAt})
	}
	return page, nil
}

func (f *fakeSource) Snapshots(_ context.Context, _ string, nodeIDs []string) ([]pullrequest.Snapshot, error) {
	var out []pullrequest.Snapshot
	for _, id := range nodeIDs {
		for _, s := range f.snapshots {
			if s.NodeID == id {
				out = append(out, s)
				f.fetched = append(f.fetched, s.Number)
			}
		}
	}
	return out, nil
}

type fakeTracker struct{ skipped string }

func (f *fakeTracker) SetTotal(context.Context, int)           {}
func (f *fakeTracker) SetCurrent(context.Context, int, string) {}
func (f *fakeTracker) Skip(_ context.Context, message string)  { f.skipped = message }
func (f *fakeTracker) Fail(context.Context, string)            {}
func (f *fakeTracker) Complete(context.Context)                {}
func (f *fakeTracker) ForOperation(task.Operation, map[string]any) handler.Tracker {
	return f
}

type fixture struct {
	handler *Sync
	source  *fakeSource
	tracker *fakeTracker
	service *service.PullRequests
	prs     persistence.PullRequestStore
	cursors persistence.PullRequestCursorStore
	repo    repository.Repository
}

func newFixture(t *testing.T, remoteURL string, interval time.Duration) *fixture {
	t.Helper()
	db := testdb.New(t)
	repos := persistence.NewRepositoryStore(db)
	repo, err := repository.NewRepository(remoteURL)
	require.NoError(t, err)
	repo, err = repos.Save(context.Background(), repo)
	require.NoError(t, err)

	index, err := persistence.NewSQLiteBM25StoreForTable(db, persistence.SQLitePullRequestBM25Table, zerolog.Nop())
	require.NoError(t, err)
	associations := persistence.NewAssociationStore(db)
	enrichments := service.NewEnrichment(persistence.NewEnrichmentStore(db), associations, nil, nil, nil, nil, nil, index)
	prs := persistence.NewPullRequestStore(db)
	cursors := persistence.NewPullRequestCursorStore(db)
	svc := service.NewPullRequests(prs, enrichments, associations, repos, index)

	source := newFakeSource()
	tracker := &fakeTracker{}
	h, err := NewSync(Stores{Repositories: repos, PullRequests: prs, Cursors: cursors}, source, svc, interval, tracker, zerolog.Nop())
	require.NoError(t, err)

	return &fixture{handler: h, source: source, tracker: tracker, service: svc, prs: prs, cursors: cursors, repo: repo}
}

func (f *fixture) sync(t *testing.T) error {
	t.Helper()
	f.source.fetched = nil
	f.source.pagesServed = 0
	return f.handler.Execute(context.Background(), map[string]any{"repository_id": f.repo.ID()})
}

func (f *fixture) numbers(t *testing.T, query string) []int {
	t.Helper()
	hits, err := f.service.Search(context.Background(), query, 10, nil)
	require.NoError(t, err)
	out := make([]int, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.PullRequest().Number())
	}
	sort.Ints(out)
	return out
}

const remote = "https://x-access-token:tok@github.com/acme/app.git"

var base = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func TestSync_FirstPassIndexesOpenAndMergedPullRequests(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	f.source.put(1, pullrequest.StateMerged, base.Add(1*time.Hour), "introduces the tenant headings")
	f.source.put(2, pullrequest.StateOpen, base.Add(2*time.Hour), "draft tenant switcher")
	f.source.put(3, pullrequest.StateClosed, base.Add(3*time.Hour), "abandoned tenant idea")

	require.NoError(t, f.sync(t))

	assert.ElementsMatch(t, []int{1, 2}, f.source.fetched)
	assert.Equal(t, []int{1, 2}, f.numbers(t, "tenant"))

	cursor, err := f.cursors.FindOne(context.Background(), repository.WithRepoID(f.repo.ID()))
	require.NoError(t, err)
	assert.True(t, cursor.Watermark().Equal(base.Add(3*time.Hour)))
	assert.False(t, cursor.FullSyncedAt().IsZero())
	assert.True(t, cursor.FullStartedAt().IsZero())
}

func TestSync_IncrementalPassFetchesOnlyChangedPullRequests(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	for i := 1; i <= 5; i++ {
		f.source.put(i, pullrequest.StateMerged, base.Add(time.Duration(i)*time.Hour), fmt.Sprintf("original body %d", i))
	}
	require.NoError(t, f.sync(t))

	f.source.put(2, pullrequest.StateMerged, base.Add(10*time.Hour), "rewritten with caching rationale")
	f.source.put(6, pullrequest.StateOpen, base.Add(11*time.Hour), "new caching proposal")

	require.NoError(t, f.sync(t))

	assert.ElementsMatch(t, []int{2, 6}, f.source.fetched)
	assert.Equal(t, 2, f.source.pagesServed, "listing stops at the watermark instead of reading every page")
	assert.Equal(t, []int{2, 6}, f.numbers(t, "caching"))
	assert.Equal(t, []int{1, 3, 4, 5}, f.numbers(t, "original"), "the old text of a re-fetched pull request is replaced")
}

func TestSync_UnchangedRepositoryFetchesNothing(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	f.source.put(1, pullrequest.StateMerged, base, "body")
	require.NoError(t, f.sync(t))

	require.NoError(t, f.sync(t))

	assert.Empty(t, f.source.fetched)
	assert.Equal(t, 1, f.source.pagesServed)
}

func TestSync_PullRequestClosedWithoutMergeIsRemoved(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	f.source.put(1, pullrequest.StateOpen, base, "experimental parser")
	require.NoError(t, f.sync(t))
	require.Equal(t, []int{1}, f.numbers(t, "parser"))

	f.source.put(1, pullrequest.StateClosed, base.Add(time.Hour), "experimental parser")
	require.NoError(t, f.sync(t))

	assert.Empty(t, f.numbers(t, "parser"))
	count, err := f.prs.Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSync_FullPassRefetchesEverythingAndRemovesVanished(t *testing.T) {
	f := newFixture(t, remote, time.Nanosecond)
	f.source.put(1, pullrequest.StateMerged, base, "first parser")
	f.source.put(2, pullrequest.StateMerged, base.Add(time.Hour), "second parser")
	require.NoError(t, f.sync(t))

	delete(f.source.snapshots, 1)
	require.NoError(t, f.sync(t))

	assert.Equal(t, []int{2}, f.source.fetched, "a full pass re-fetches unchanged pull requests")
	assert.Equal(t, []int{2}, f.numbers(t, "parser"))
}

func TestSync_InterruptedFullPassResumes(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	for i := 1; i <= 4; i++ {
		f.source.put(i, pullrequest.StateMerged, base.Add(time.Duration(i)*time.Hour), "parser change")
	}
	f.source.failOnPage = 2

	err := f.sync(t)
	require.ErrorIs(t, err, errRateLimited)
	assert.ElementsMatch(t, []int{3, 4}, f.source.fetched, "the first page is indexed before the failure")

	cursor, err := f.cursors.FindOne(context.Background(), repository.WithRepoID(f.repo.ID()))
	require.NoError(t, err)
	assert.False(t, cursor.FullStartedAt().IsZero())
	assert.True(t, cursor.Watermark().IsZero(), "the watermark only moves when a pass completes")

	f.source.failOnPage = 0
	require.NoError(t, f.sync(t))

	assert.ElementsMatch(t, []int{1, 2}, f.source.fetched, "the resumed pass skips pull requests already fetched")
	assert.Equal(t, []int{1, 2, 3, 4}, f.numbers(t, "parser"))
}

func TestSync_RepositoryNotOnGitHubIsSkipped(t *testing.T) {
	f := newFixture(t, "https://gitlab.com/acme/app.git", 24*time.Hour)
	f.source.err = pullrequest.ErrUnsupportedRemote

	require.NoError(t, f.sync(t))

	assert.Equal(t, "Repository is not hosted on GitHub", f.tracker.skipped)
}

func TestSync_SourceErrorFailsTask(t *testing.T) {
	f := newFixture(t, "https://github.com/acme/app.git", 24*time.Hour)
	missingToken := errors.New("github remote URL carries no token")
	f.source.err = missingToken

	err := f.sync(t)

	assert.ErrorIs(t, err, missingToken)
}

func TestNewSync_RejectsNonPositiveInterval(t *testing.T) {
	f := newFixture(t, remote, time.Hour)
	_, err := NewSync(f.handler.stores, f.source, f.service, 0, f.tracker, zerolog.Nop())
	assert.Error(t, err)
}

func TestSync_SnapshotClosedSinceListingIsRemoved(t *testing.T) {
	f := newFixture(t, remote, 24*time.Hour)
	f.source.put(1, pullrequest.StateOpen, base, "racy parser")
	require.NoError(t, f.sync(t))
	require.Equal(t, []int{1}, f.numbers(t, "parser"))

	f.source.put(1, pullrequest.StateClosed, base.Add(time.Hour), "racy parser")
	f.source.listed = map[int]pullrequest.State{1: pullrequest.StateOpen}
	require.NoError(t, f.sync(t))

	assert.Equal(t, []int{1}, f.source.fetched)
	assert.Empty(t, f.numbers(t, "parser"))
}

func TestSync_FullPassKeepsPullRequestUpdatedDuringListing(t *testing.T) {
	f := newFixture(t, remote, time.Nanosecond)
	for i := 1; i <= 4; i++ {
		f.source.put(i, pullrequest.StateMerged, base.Add(time.Duration(i)*time.Hour), "parser change")
	}
	require.NoError(t, f.sync(t))

	f.source.beforePage = func(page int) {
		if page == 2 {
			f.source.put(1, pullrequest.StateMerged, time.Now(), "parser change edited")
		}
	}
	require.NoError(t, f.sync(t))

	assert.Equal(t, []int{1, 2, 3, 4}, f.numbers(t, "parser"), "a pull request that moved ahead of the listing is not removed")
	assert.Equal(t, []int{1}, f.numbers(t, "edited"))
}
