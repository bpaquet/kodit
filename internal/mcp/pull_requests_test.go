package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/application/service"
	"github.com/helixml/kodit/domain/enrichment"
	"github.com/helixml/kodit/domain/pullrequest"
	"github.com/helixml/kodit/domain/repository"
	"github.com/helixml/kodit/domain/sourcelocation"
)

type searchCall struct {
	query   string
	limit   int
	repoIDs []int64
}

type fakePullRequests struct {
	hits     []pullrequest.Hit
	searches []searchCall
	gets     []int
}

func (f *fakePullRequests) Search(_ context.Context, query string, limit int, repoIDs []int64) ([]pullrequest.Hit, error) {
	f.searches = append(f.searches, searchCall{query: query, limit: limit, repoIDs: repoIDs})
	return f.hits, nil
}

func (f *fakePullRequests) Get(_ context.Context, repoID int64, number int) (pullrequest.Hit, error) {
	f.gets = append(f.gets, number)
	for _, h := range f.hits {
		if h.PullRequest().RepoID() == repoID && h.PullRequest().Number() == number {
			return h, nil
		}
	}
	return pullrequest.Hit{}, fmt.Errorf("pull request #%d: %w", number, service.ErrPullRequestNotFound)
}

func mergedHit() pullrequest.Hit {
	created := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	pr := pullrequest.ReconstructPullRequest(1, 1, 42, 9, "Move headings to config", "https://github.com/example/repo/pull/42",
		pullrequest.StateMerged, "alice", "abc123", created, created.Add(48*time.Hour), created.Add(24*time.Hour), created)
	doc := "Pull request #42: Move headings to config\nRepository: https://github.com/example/repo\n\n## Description\n\nBecause tenants rename them." +
		strings.Repeat(" é", 1500)
	return pullrequest.NewHit(pr, "https://github.com/example/repo", doc, 7.5)
}

func pullRequestServer(prs pullrequest.Searcher) *Server {
	var opts []ServerOption
	if prs != nil {
		opts = append(opts, WithPullRequests(prs))
	}
	return NewServer(
		&fakeRepositoryLister{repos: []repository.Repository{testRepo()}},
		&fakeCommitFinder{commits: []repository.Commit{testCommit()}},
		&fakeEnrichmentQuery{enrichments: []enrichment.Enrichment{}},
		&fakeFileContentReader{},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		nil,
		&fakeEnrichmentResolver{
			sourceFiles:   map[string][]int64{},
			lineRanges:    map[string]sourcelocation.SourceLocation{},
			repositoryIDs: map[string]int64{},
		},
		&fakeFileLister{},
		&fakeFileFinder{},
		&fakeGrepper{},
		"1.0.0-test",
		zerolog.Nop(),
		opts...,
	)
}

func callPullRequestTool(t *testing.T, srv *Server, name string, args map[string]any) mcp.CallToolResult {
	t.Helper()
	sendMessage(t, srv, "initialize", 1, initializeParams())
	resp := sendMessage(t, srv, "tools/call", 2, map[string]any{"name": name, "arguments": args})
	var result mcp.CallToolResult
	resultJSON(t, resp, &result)
	return result
}

func toolNames(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	sendMessage(t, srv, "initialize", 1, initializeParams())
	resp := sendMessage(t, srv, "tools/list", 2, nil)
	var result mcp.ListToolsResult
	resultJSON(t, resp, &result)
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	return names
}

func instructionsOf(t *testing.T, srv *Server) string {
	t.Helper()
	resp := sendMessage(t, srv, "initialize", 1, initializeParams())
	var init mcp.InitializeResult
	resultJSON(t, resp, &init)
	return init.Instructions
}

func TestPullRequestTools_HiddenWhenDisabled(t *testing.T) {
	names := toolNames(t, pullRequestServer(nil))
	assert.False(t, names["kodit_pull_request_search"])
	assert.False(t, names["kodit_pull_request"])
	assert.True(t, names["kodit_keyword_search"], "file keyword search is unaffected")
	assert.NotContains(t, instructionsOf(t, pullRequestServer(nil)), "kodit_pull_request_search")
}

func TestPullRequestTools_ListedWhenEnabled(t *testing.T) {
	names := toolNames(t, pullRequestServer(&fakePullRequests{}))
	assert.True(t, names["kodit_pull_request_search"])
	assert.True(t, names["kodit_pull_request"])
	assert.Contains(t, instructionsOf(t, pullRequestServer(&fakePullRequests{})), "kodit_pull_request_search")
}

func TestPullRequestSearch_FormatsHits(t *testing.T) {
	prs := &fakePullRequests{hits: []pullrequest.Hit{mergedHit()}}

	result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request_search", map[string]any{"keywords": "tenant headings"})
	require.False(t, result.IsError, textFromContent(t, result))

	var hits []pullRequestResult
	require.NoError(t, json.Unmarshal([]byte(textFromContent(t, result)), &hits))
	require.Len(t, hits, 1)
	h := hits[0]
	assert.Equal(t, "https://github.com/example/repo", h.Repository)
	assert.Equal(t, 42, h.Number)
	assert.Equal(t, "Move headings to config", h.Title)
	assert.Equal(t, "https://github.com/example/repo/pull/42", h.URL)
	assert.Equal(t, "MERGED", h.State)
	assert.Equal(t, "alice", h.Author)
	assert.Equal(t, "2026-03-01T09:00:00Z", h.CreatedAt)
	assert.Equal(t, "2026-03-02T09:00:00Z", h.MergedAt)
	assert.Equal(t, "abc123", h.MergeCommitSHA)
	assert.Equal(t, 7.5, h.Score)
	assert.True(t, strings.HasPrefix(h.Excerpt, "Because tenants rename them."), "excerpt starts at the description")
	assert.True(t, strings.HasSuffix(h.Excerpt, "..."))
	assert.LessOrEqual(t, len(h.Excerpt), pullRequestExcerptLength+3)

	require.Len(t, prs.searches, 1)
	assert.Equal(t, searchCall{query: "tenant headings", limit: 10}, prs.searches[0])
}

func TestPullRequestSearch_LimitAndRepositoryFilter(t *testing.T) {
	prs := &fakePullRequests{}

	result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request_search", map[string]any{
		"keywords": "x", "limit": 500, "source_repo": "https://github.com/example/repo",
	})
	require.False(t, result.IsError)
	assert.Equal(t, "[]", textFromContent(t, result))

	require.Len(t, prs.searches, 1)
	assert.Equal(t, maxSearchLimit, prs.searches[0].limit)
	assert.Equal(t, []int64{1}, prs.searches[0].repoIDs)
}

func TestPullRequestSearch_UnknownRepositoryReturnsNothing(t *testing.T) {
	prs := &fakePullRequests{hits: []pullrequest.Hit{mergedHit()}}

	result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request_search", map[string]any{
		"keywords": "x", "source_repo": "https://github.com/example/unknown",
	})

	require.False(t, result.IsError)
	assert.Equal(t, "[]", textFromContent(t, result))
	assert.Empty(t, prs.searches, "an unknown repository must not widen the search to every repository")
}

func TestPullRequestSearch_Validation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing keywords", args: map[string]any{}, want: "keywords is required"},
		{name: "blank keywords", args: map[string]any{"keywords": "  "}, want: "keywords must not be empty"},
		{name: "negative limit", args: map[string]any{"keywords": "x", "limit": -1}, want: "limit must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := &fakePullRequests{}
			result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request_search", tt.args)
			assert.True(t, result.IsError)
			assert.Contains(t, textFromContent(t, result), tt.want)
			assert.Empty(t, prs.searches)
		})
	}

	prs := &fakePullRequests{}
	result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request_search", map[string]any{"keywords": "x", "limit": 0})
	assert.False(t, result.IsError)
	assert.Equal(t, "[]", textFromContent(t, result))
	assert.Empty(t, prs.searches)
}

func TestGetPullRequest(t *testing.T) {
	prs := &fakePullRequests{hits: []pullrequest.Hit{mergedHit()}}

	result := callPullRequestTool(t, pullRequestServer(prs), "kodit_pull_request", map[string]any{
		"repo_url": "https://github.com/example/repo", "number": 42,
	})

	require.False(t, result.IsError, textFromContent(t, result))
	assert.Equal(t, mergedHit().Document(), textFromContent(t, result))
}

func TestGetPullRequest_Errors(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing repo", args: map[string]any{"number": 1}, want: "repo_url is required"},
		{name: "missing number", args: map[string]any{"repo_url": "https://github.com/example/repo"}, want: "number is required"},
		{name: "fractional number", args: map[string]any{"repo_url": "https://github.com/example/repo", "number": 1.5}, want: "positive integer"},
		{name: "zero number", args: map[string]any{"repo_url": "https://github.com/example/repo", "number": 0}, want: "positive integer"},
		{name: "unknown repo", args: map[string]any{"repo_url": "https://github.com/example/unknown", "number": 1}, want: "repository not found"},
		{name: "not indexed", args: map[string]any{"repo_url": "https://github.com/example/repo", "number": 7}, want: "pull request #7 is not indexed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callPullRequestTool(t, pullRequestServer(&fakePullRequests{hits: []pullrequest.Hit{mergedHit()}}), "kodit_pull_request", tt.args)
			assert.True(t, result.IsError)
			assert.Contains(t, textFromContent(t, result), tt.want)
		})
	}
}

func TestScopedPullRequests(t *testing.T) {
	inner := &fakePullRequests{}
	scoped := &scopedPullRequests{inner: inner, scope: newRepoScope([]int64{1, 2})}
	ctx := context.Background()

	_, err := scoped.Search(ctx, "x", 5, nil)
	require.NoError(t, err)
	_, err = scoped.Search(ctx, "x", 5, []int64{2, 3})
	require.NoError(t, err)
	hits, err := scoped.Search(ctx, "x", 5, []int64{3})
	require.NoError(t, err)
	assert.Empty(t, hits)

	require.Len(t, inner.searches, 2, "a search outside the scope never reaches the searcher")
	assert.Equal(t, []int64{1, 2}, inner.searches[0].repoIDs)
	assert.Equal(t, []int64{2}, inner.searches[1].repoIDs)

	_, err = scoped.Get(ctx, 3, 1)
	assert.Error(t, err)
	assert.Empty(t, inner.gets)
}
