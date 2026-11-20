package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/helixml/kodit/domain/pullrequest"
)

// DefaultEndpoint is the GitHub GraphQL API endpoint.
const DefaultEndpoint = "https://api.github.com/graphql"

// ErrRateLimited is returned when GitHub refuses a request for exceeding the
// token's rate limit.
var ErrRateLimited = errors.New("github rate limit exceeded")

const (
	maxFiles           = 100
	maxComments        = 100
	maxReviews         = 100
	maxThreads         = 50
	maxThreadComments  = 30
	maxErrorBodyLength = 512
	maxResponseBytes   = 64 << 20
)

const summariesQuery = `query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes { id number state updatedAt }
    }
  }
}`

var snapshotsQuery = fmt.Sprintf(`query($ids: [ID!]!) {
  nodes(ids: $ids) {
    ... on PullRequest {
      id number title body url state createdAt updatedAt mergedAt
      author { login }
      baseRefName headRefName
      mergeCommit { oid }
      files(first: %d) { totalCount nodes { path } }
      comments(first: %d) { totalCount nodes { author { login } body createdAt } }
      reviews(first: %d) { totalCount nodes { author { login } state body submittedAt } }
      reviewThreads(first: %d) {
        totalCount
        nodes {
          path line isResolved
          comments(first: %d) { totalCount nodes { author { login } body createdAt } }
        }
      }
    }
  }
}`, maxFiles, maxComments, maxReviews, maxThreads, maxThreadComments)

// Client reads pull requests through the GitHub GraphQL API, authenticating
// with the token carried by each repository's remote URL.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient creates a Client for the public GitHub API.
func NewClient() *Client {
	return NewClientForEndpoint(DefaultEndpoint, &http.Client{Timeout: 2 * time.Minute})
}

// NewClientForEndpoint creates a Client for the given GraphQL endpoint.
func NewClientForEndpoint(endpoint string, httpClient *http.Client) *Client {
	return &Client{endpoint: endpoint, http: httpClient}
}

// Summaries returns one page of the repository's pull requests, most
// recently updated first, starting after cursor (empty for the first page).
func (c *Client) Summaries(ctx context.Context, remoteURL string, cursor string) (pullrequest.SummaryPage, error) {
	remote, err := ParseRemote(remoteURL)
	if err != nil {
		return pullrequest.SummaryPage{}, err
	}

	variables := map[string]any{"owner": remote.owner, "name": remote.name, "cursor": nil}
	if cursor != "" {
		variables["cursor"] = cursor
	}

	var data summariesData
	if err := c.query(ctx, remote.token, summariesQuery, variables, &data); err != nil {
		return pullrequest.SummaryPage{}, err
	}
	if data.Repository == nil {
		return pullrequest.SummaryPage{}, fmt.Errorf("github repository %s/%s not found", remote.owner, remote.name)
	}

	connection := data.Repository.PullRequests
	summaries := make([]pullrequest.Summary, 0, len(connection.Nodes))
	for _, n := range connection.Nodes {
		summaries = append(summaries, pullrequest.Summary{
			NodeID:    n.ID,
			Number:    n.Number,
			State:     pullrequest.State(n.State),
			UpdatedAt: n.UpdatedAt,
		})
	}

	return pullrequest.SummaryPage{
		Summaries: summaries,
		Cursor:    connection.PageInfo.EndCursor,
		HasNext:   connection.PageInfo.HasNextPage,
	}, nil
}

// Snapshots returns the pull requests with the given node IDs, with their
// discussion. Pull requests that no longer exist are left out.
func (c *Client) Snapshots(ctx context.Context, remoteURL string, nodeIDs []string) ([]pullrequest.Snapshot, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	remote, err := ParseRemote(remoteURL)
	if err != nil {
		return nil, err
	}

	var data snapshotsData
	if err := c.query(ctx, remote.token, snapshotsQuery, map[string]any{"ids": nodeIDs}, &data); err != nil {
		return nil, err
	}

	snapshots := make([]pullrequest.Snapshot, 0, len(data.Nodes))
	for _, n := range data.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		snapshots = append(snapshots, n.snapshot())
	}
	return snapshots, nil
}

func (c *Client) query(ctx context.Context, token, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("encode github query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kodit")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read github response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("github response exceeds %d bytes", maxResponseBytes)
	}

	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		return fmt.Errorf("%w: resets at %s", ErrRateLimited, resetTime(resp.Header.Get("X-RateLimit-Reset")))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github API returned %d: %s", resp.StatusCode, truncate(string(payload), maxErrorBodyLength))
	}

	var envelope graphQLResponse
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		rateLimited := false
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
			if e.Type == "RATE_LIMITED" {
				rateLimited = true
			}
		}
		if rateLimited {
			return fmt.Errorf("%w: %s", ErrRateLimited, strings.Join(messages, "; "))
		}
		return fmt.Errorf("github API error: %s", strings.Join(messages, "; "))
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode github data: %w", err)
	}
	return nil
}

func resetTime(header string) string {
	var seconds int64
	if _, err := fmt.Sscan(header, &seconds); err != nil || seconds == 0 {
		return "unknown"
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type summariesData struct {
	Repository *struct {
		PullRequests struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				ID        string    `json:"id"`
				Number    int       `json:"number"`
				State     string    `json:"state"`
				UpdatedAt time.Time `json:"updatedAt"`
			} `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

type snapshotsData struct {
	Nodes []*pullRequestNode `json:"nodes"`
}

type actor struct {
	Login string `json:"login"`
}

func login(a *actor) string {
	if a == nil || a.Login == "" {
		return "ghost"
	}
	return a.Login
}

type commentNode struct {
	Author    *actor    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type commentConnection struct {
	TotalCount int           `json:"totalCount"`
	Nodes      []commentNode `json:"nodes"`
}

func (c commentConnection) comments() []pullrequest.Comment {
	out := make([]pullrequest.Comment, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, pullrequest.Comment{Author: login(n.Author), Body: n.Body, CreatedAt: n.CreatedAt})
	}
	return out
}

type pullRequestNode struct {
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	MergedAt    *time.Time `json:"mergedAt"`
	Author      *actor     `json:"author"`
	BaseRefName string     `json:"baseRefName"`
	HeadRefName string     `json:"headRefName"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Files struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
	Comments commentConnection `json:"comments"`
	Reviews  struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author      *actor    `json:"author"`
			State       string    `json:"state"`
			Body        string    `json:"body"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Path       string            `json:"path"`
			Line       *int              `json:"line"`
			IsResolved bool              `json:"isResolved"`
			Comments   commentConnection `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

func (n pullRequestNode) snapshot() pullrequest.Snapshot {
	s := pullrequest.Snapshot{
		NodeID:       n.ID,
		Number:       n.Number,
		Title:        n.Title,
		Body:         n.Body,
		URL:          n.URL,
		State:        pullrequest.State(n.State),
		Author:       login(n.Author),
		BaseRef:      n.BaseRefName,
		HeadRef:      n.HeadRefName,
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
		FileCount:    n.Files.TotalCount,
		Comments:     n.Comments.comments(),
		CommentCount: n.Comments.TotalCount,
		ReviewCount:  n.Reviews.TotalCount,
		ThreadCount:  n.ReviewThreads.TotalCount,
	}
	if n.MergedAt != nil {
		s.MergedAt = *n.MergedAt
	}
	if n.MergeCommit != nil {
		s.MergeCommitSHA = n.MergeCommit.OID
	}
	for _, f := range n.Files.Nodes {
		s.Files = append(s.Files, f.Path)
	}
	for _, r := range n.Reviews.Nodes {
		s.Reviews = append(s.Reviews, pullrequest.Review{Author: login(r.Author), State: r.State, Body: r.Body, SubmittedAt: r.SubmittedAt})
	}
	for _, t := range n.ReviewThreads.Nodes {
		line := 0
		if t.Line != nil {
			line = *t.Line
		}
		s.Threads = append(s.Threads, pullrequest.Thread{
			Path:         t.Path,
			Line:         line,
			Resolved:     t.IsResolved,
			Comments:     t.Comments.comments(),
			CommentCount: t.Comments.TotalCount,
		})
	}
	return s
}

// Ensure Client implements pullrequest.Source.
var _ pullrequest.Source = (*Client)(nil)
