package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/domain/pullrequest"
)

const remoteURL = "https://x-access-token:secret-token@github.com/acme/app.git"

type recordedRequest struct {
	authorization string
	query         string
	variables     map[string]any
}

func graphQLServer(t *testing.T, status int, headers map[string]string, body string) (*Client, *[]recordedRequest) {
	t.Helper()
	var requests []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req graphQLRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		requests = append(requests, recordedRequest{
			authorization: r.Header.Get("Authorization"),
			query:         req.Query,
			variables:     req.Variables,
		})
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClientForEndpoint(srv.URL, srv.Client()), &requests
}

func TestClient_Summaries(t *testing.T) {
	client, requests := graphQLServer(t, http.StatusOK, nil, `{"data":{"repository":{"pullRequests":{
		"pageInfo":{"hasNextPage":true,"endCursor":"CUR2"},
		"nodes":[
			{"id":"PR_2","number":2,"state":"MERGED","updatedAt":"2026-11-20T10:00:00Z"},
			{"id":"PR_1","number":1,"state":"CLOSED","updatedAt":"2026-11-19T10:00:00Z"}
		]}}}}`)

	page, err := client.Summaries(context.Background(), remoteURL, "CUR1")
	require.NoError(t, err)

	assert.Equal(t, []pullrequest.Summary{
		{NodeID: "PR_2", Number: 2, State: pullrequest.StateMerged, UpdatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)},
		{NodeID: "PR_1", Number: 1, State: pullrequest.StateClosed, UpdatedAt: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)},
	}, page.Summaries)
	assert.True(t, page.HasNext)
	assert.Equal(t, "CUR2", page.Cursor)

	require.Len(t, *requests, 1)
	req := (*requests)[0]
	assert.Equal(t, "Bearer secret-token", req.authorization)
	assert.Equal(t, map[string]any{"owner": "acme", "name": "app", "cursor": "CUR1"}, req.variables)
	assert.Contains(t, req.query, "orderBy: {field: UPDATED_AT, direction: DESC}")
}

func TestClient_SummariesFirstPageSendsNullCursor(t *testing.T) {
	client, requests := graphQLServer(t, http.StatusOK, nil, `{"data":{"repository":{"pullRequests":{"pageInfo":{},"nodes":[]}}}}`)

	_, err := client.Summaries(context.Background(), remoteURL, "")
	require.NoError(t, err)

	assert.Nil(t, (*requests)[0].variables["cursor"])
}

func TestClient_SummariesUnknownRepository(t *testing.T) {
	client, _ := graphQLServer(t, http.StatusOK, nil, `{"data":{"repository":null}}`)

	_, err := client.Summaries(context.Background(), remoteURL, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/app not found")
}

func TestClient_Snapshots(t *testing.T) {
	client, requests := graphQLServer(t, http.StatusOK, nil, `{"data":{"nodes":[
		{"id":"PR_1","number":1,"title":"Add cache","body":"Because the API is slow.","url":"https://github.com/acme/app/pull/1",
		 "state":"MERGED","createdAt":"2026-11-01T00:00:00Z","updatedAt":"2026-11-03T00:00:00Z","mergedAt":"2026-11-02T00:00:00Z",
		 "author":{"login":"alice"},"baseRefName":"main","headRefName":"cache","mergeCommit":{"oid":"deadbeef"},
		 "files":{"totalCount":1,"nodes":[{"path":"cache.go"}]},
		 "comments":{"totalCount":1,"nodes":[{"author":null,"body":"nice","createdAt":"2026-11-01T01:00:00Z"}]},
		 "reviews":{"totalCount":1,"nodes":[{"author":{"login":"bob"},"state":"APPROVED","body":"ok","submittedAt":"2026-11-01T02:00:00Z"}]},
		 "reviewThreads":{"totalCount":1,"nodes":[{"path":"cache.go","line":null,"isResolved":false,
		   "comments":{"totalCount":3,"nodes":[{"author":{"login":"carol"},"body":"ttl?","createdAt":"2026-11-01T03:00:00Z"}]}}]}},
		null
	]}}`)

	snapshots, err := client.Snapshots(context.Background(), remoteURL, []string{"PR_1", "PR_GONE"})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)

	s := snapshots[0]
	assert.Equal(t, "PR_1", s.NodeID)
	assert.Equal(t, 1, s.Number)
	assert.Equal(t, "Add cache", s.Title)
	assert.Equal(t, "Because the API is slow.", s.Body)
	assert.Equal(t, pullrequest.StateMerged, s.State)
	assert.Equal(t, "alice", s.Author)
	assert.Equal(t, "deadbeef", s.MergeCommitSHA)
	assert.Equal(t, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), s.MergedAt)
	assert.Equal(t, []string{"cache.go"}, s.Files)
	assert.Equal(t, "ghost", s.Comments[0].Author, "deleted accounts show as ghost")
	assert.Equal(t, "bob", s.Reviews[0].Author)
	require.Len(t, s.Threads, 1)
	assert.Equal(t, 0, s.Threads[0].Line)
	assert.Equal(t, 3, s.Threads[0].CommentCount)
	assert.Equal(t, "carol", s.Threads[0].Comments[0].Author)

	assert.Equal(t, []any{"PR_1", "PR_GONE"}, (*requests)[0].variables["ids"])
}

func TestClient_SnapshotsWithoutIDsMakesNoRequest(t *testing.T) {
	client, requests := graphQLServer(t, http.StatusOK, nil, `{}`)

	snapshots, err := client.Snapshots(context.Background(), remoteURL, nil)

	require.NoError(t, err)
	assert.Empty(t, snapshots)
	assert.Empty(t, *requests)
}

func TestClient_RateLimited(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
	}{
		{name: "primary limit", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1795000000"}, body: `{}`},
		{name: "too many requests", status: http.StatusTooManyRequests, body: `{}`},
		{name: "graphql rate limited", status: http.StatusOK, body: `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := graphQLServer(t, tt.status, tt.headers, tt.body)

			_, err := client.Summaries(context.Background(), remoteURL, "")

			assert.True(t, errors.Is(err, ErrRateLimited), "got %v", err)
		})
	}
}

func TestClient_ErrorsDoNotLeakToken(t *testing.T) {
	client, _ := graphQLServer(t, http.StatusUnauthorized, nil, `{"message":"Bad credentials"}`)

	_, err := client.Summaries(context.Background(), remoteURL, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.False(t, strings.Contains(err.Error(), "secret-token"))
}

func TestClient_GraphQLErrors(t *testing.T) {
	client, _ := graphQLServer(t, http.StatusOK, nil, `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`)

	_, err := client.Summaries(context.Background(), remoteURL, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource not accessible by personal access token")
	assert.False(t, errors.Is(err, ErrRateLimited))
}

func TestClient_RejectsRemoteBeforeRequest(t *testing.T) {
	client, requests := graphQLServer(t, http.StatusOK, nil, `{}`)

	_, err := client.Summaries(context.Background(), "https://gitlab.com/acme/app.git", "")
	assert.ErrorIs(t, err, pullrequest.ErrUnsupportedRemote)

	_, err = client.Snapshots(context.Background(), "https://github.com/acme/app.git", []string{"PR_1"})
	assert.ErrorIs(t, err, ErrMissingToken)

	assert.Empty(t, *requests)
}
