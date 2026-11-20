package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/helixml/kodit/application/service"
	"github.com/helixml/kodit/domain/pullrequest"
)

const pullRequestExcerptLength = 2000

// pullRequestResult is one kodit_pull_request_search hit.
type pullRequestResult struct {
	Repository     string  `json:"repository"`
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	State          string  `json:"state"`
	Author         string  `json:"author"`
	CreatedAt      string  `json:"created_at"`
	MergedAt       string  `json:"merged_at,omitempty"`
	MergeCommitSHA string  `json:"merge_commit_sha,omitempty"`
	Score          float64 `json:"score"`
	Excerpt        string  `json:"excerpt"`
}

func (s *Server) handlePullRequestSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	keywords, err := request.RequireString("keywords")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("keywords is required: %v", err)), nil
	}
	if strings.TrimSpace(keywords) == "" {
		return mcp.NewToolResultError("keywords must not be empty"), nil
	}

	limit := int(request.GetFloat("limit", 10))
	if limit < 0 {
		return mcp.NewToolResultError("limit must not be negative"), nil
	}
	if limit == 0 {
		return mcp.NewToolResultText("[]"), nil
	}
	limit = min(limit, maxSearchLimit)

	var repoIDs []int64
	if repoURL := request.GetString("source_repo", ""); repoURL != "" {
		repos, err := s.resolveRepository(ctx, repoURL)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to find repository: %v", err)), nil
		}
		if len(repos) == 0 {
			return mcp.NewToolResultText("[]"), nil
		}
		repoIDs = []int64{repos[0].ID()}
	}

	hits, err := s.pullRequests.Search(ctx, keywords, limit, repoIDs)
	if err != nil {
		s.logger.Error().Interface("error", err).Msg("pull request search failed")
		return mcp.NewToolResultError(fmt.Sprintf("pull request search failed: %v", err)), nil
	}

	results := make([]pullRequestResult, 0, len(hits))
	for _, h := range hits {
		pr := h.PullRequest()
		result := pullRequestResult{
			Repository:     h.RepoURL(),
			Number:         pr.Number(),
			Title:          pr.Title(),
			URL:            pr.URL(),
			State:          string(pr.State()),
			Author:         pr.Author(),
			CreatedAt:      pr.CreatedAt().UTC().Format(time.RFC3339),
			MergeCommitSHA: pr.MergeCommitSHA(),
			Score:          h.Score(),
			Excerpt:        h.Excerpt(pullRequestExcerptLength),
		}
		if !pr.MergedAt().IsZero() {
			result.MergedAt = pr.MergedAt().UTC().Format(time.RFC3339)
		}
		results = append(results, result)
	}

	body, err := json.Marshal(results)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal results: %v", err)), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}

func (s *Server) handleGetPullRequest(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoURL, err := request.RequireString("repo_url")
	if err != nil {
		return mcp.NewToolResultError("repo_url is required"), nil
	}
	number, err := request.RequireFloat("number")
	if err != nil {
		return mcp.NewToolResultError("number is required"), nil
	}
	if number < 1 || number != float64(int(number)) {
		return mcp.NewToolResultError("number must be a positive integer"), nil
	}

	repos, err := s.resolveRepository(ctx, repoURL)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to find repository: %v", err)), nil
	}
	if len(repos) == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("repository not found: %s", repoURL)), nil
	}

	hit, err := s.pullRequests.Get(ctx, repos[0].ID(), int(number))
	if errors.Is(err, service.ErrPullRequestNotFound) {
		return mcp.NewToolResultError(fmt.Sprintf("pull request #%d is not indexed for %s", int(number), repoURL)), nil
	}
	if err != nil {
		s.logger.Error().Interface("error", err).Msg("failed to get pull request")
		return mcp.NewToolResultError(fmt.Sprintf("failed to get pull request: %v", err)), nil
	}
	return mcp.NewToolResultText(hit.Document()), nil
}

// scopedPullRequests restricts pull request search and reads to the scope.
type scopedPullRequests struct {
	inner pullrequest.Searcher
	scope repoScope
}

func (s *scopedPullRequests) Search(ctx context.Context, query string, limit int, repoIDs []int64) ([]pullrequest.Hit, error) {
	allowed := s.scope.ids
	if len(repoIDs) > 0 {
		allowed = make([]int64, 0, len(repoIDs))
		for _, id := range repoIDs {
			if s.scope.contains(id) {
				allowed = append(allowed, id)
			}
		}
	}
	if len(allowed) == 0 {
		return []pullrequest.Hit{}, nil
	}
	return s.inner.Search(ctx, query, limit, allowed)
}

func (s *scopedPullRequests) Get(ctx context.Context, repoID int64, number int) (pullrequest.Hit, error) {
	if err := s.scope.check(repoID); err != nil {
		return pullrequest.Hit{}, err
	}
	return s.inner.Get(ctx, repoID, number)
}
