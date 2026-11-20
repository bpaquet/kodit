package pullrequest

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func snapshotFixture() Snapshot {
	created := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	return Snapshot{
		NodeID:         "PR_1",
		Number:         42,
		Title:          "Move section headings to config",
		Body:           "Headings come from the template because each tenant renames them.",
		URL:            "https://github.com/acme/app/pull/42",
		State:          StateMerged,
		Author:         "alice",
		BaseRef:        "main",
		HeadRef:        "feature/headings",
		MergeCommitSHA: "abc123",
		CreatedAt:      created,
		UpdatedAt:      created.Add(48 * time.Hour),
		MergedAt:       created.Add(24 * time.Hour),
		Files:          []string{"app/sections.py", "config/sections.yaml"},
		FileCount:      3,
		Comments:       []Comment{{Author: "bob", Body: "Why not hardcode them?", CreatedAt: created}},
		CommentCount:   1,
		Reviews: []Review{
			{Author: "carol", State: "APPROVED", Body: "Config keeps tenants happy.", SubmittedAt: created},
			{Author: "dave", State: "APPROVED", Body: "  ", SubmittedAt: created},
		},
		ReviewCount: 2,
		Threads: []Thread{{
			Path:         "app/sections.py",
			Line:         12,
			Resolved:     true,
			Comments:     []Comment{{Author: "erin", Body: "Rename this variable", CreatedAt: created}},
			CommentCount: 2,
		}},
		ThreadCount: 1,
	}
}

func TestSnapshot_DocumentContainsEverySearchableField(t *testing.T) {
	doc := snapshotFixture().Document("https://github.com/acme/app")

	for _, want := range []string{
		"Pull request #42: Move section headings to config",
		"Repository: https://github.com/acme/app",
		"URL: https://github.com/acme/app/pull/42",
		"State: MERGED",
		"Author: alice",
		"Created: 2026-03-01",
		"Merged: 2026-03-02",
		"Merge commit: abc123",
		"Branches: main <- feature/headings",
		"## Description\n\nHeadings come from the template because each tenant renames them.",
		"## Files changed (3)",
		"- app/sections.py",
		"(1 more files not shown)",
		"## Conversation",
		"bob (2026-03-01):\nWhy not hardcode them?",
		"## Reviews",
		"carol (APPROVED, 2026-03-01):\nConfig keeps tenants happy.",
		"### app/sections.py:12 (resolved)",
		"erin (2026-03-01):\nRename this variable",
		"(1 more comments not shown)",
	} {
		assert.Contains(t, doc, want)
	}
	assert.NotContains(t, doc, "dave", "reviews without text are left out")
}

func TestSnapshot_DocumentOfBareOpenPullRequest(t *testing.T) {
	s := Snapshot{Number: 7, Title: "WIP", State: StateOpen, Author: "alice", BaseRef: "main", HeadRef: "wip"}

	doc := s.Document("https://github.com/acme/app")

	assert.Contains(t, doc, "## Description\n\n(no description)\n")
	assert.NotContains(t, doc, "Merged:")
	assert.NotContains(t, doc, "Merge commit:")
	for _, section := range []string{"## Files changed", "## Conversation", "## Reviews", "## Review threads"} {
		assert.False(t, strings.Contains(doc, section), "unexpected section %s", section)
	}
}

func TestSnapshot_PullRequest(t *testing.T) {
	s := snapshotFixture()
	synced := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	pr := s.PullRequest(5, 9, synced)

	assert.Equal(t, int64(5), pr.RepoID())
	assert.Equal(t, 42, pr.Number())
	assert.Equal(t, int64(9), pr.EnrichmentID())
	assert.Equal(t, "Move section headings to config", pr.Title())
	assert.Equal(t, StateMerged, pr.State())
	assert.Equal(t, "abc123", pr.MergeCommitSHA())
	assert.Equal(t, s.UpdatedAt, pr.UpdatedAt())
	assert.Equal(t, s.MergedAt, pr.MergedAt())
	assert.Equal(t, synced, pr.SyncedAt())
}

func TestHit_Excerpt(t *testing.T) {
	hit := func(doc string) Hit { return NewHit(PullRequest{}, "", doc, 0) }

	assert.True(t, strings.HasPrefix(hit(snapshotFixture().Document("u")).Excerpt(1000), "Headings come from the template"),
		"the excerpt starts at the description")
	assert.Equal(t, "no heading", hit("no heading").Excerpt(100))
	assert.Equal(t, "é...", hit("ééééé").Excerpt(3), "cuts on a rune boundary")
}
