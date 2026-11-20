package pullrequest

import (
	"fmt"
	"strings"
	"time"
)

const descriptionHeading = "## Description\n\n"

// Snapshot is a pull request as fetched from its code host, with its
// discussion. Fields are exported because it is a plain data carrier filled
// by infrastructure adapters.
type Snapshot struct {
	NodeID         string
	Number         int
	Title          string
	Body           string
	URL            string
	State          State
	Author         string
	BaseRef        string
	HeadRef        string
	MergeCommitSHA string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       time.Time
	Files          []string
	FileCount      int
	Comments       []Comment
	CommentCount   int
	Reviews        []Review
	ReviewCount    int
	Threads        []Thread
	ThreadCount    int
}

// Comment is a comment in a pull request conversation or review thread.
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// Review is a submitted review with its summary text.
type Review struct {
	Author      string
	State       string
	Body        string
	SubmittedAt time.Time
}

// Thread is an inline review thread on a file.
type Thread struct {
	Path         string
	Line         int
	Resolved     bool
	Comments     []Comment
	CommentCount int
}

// PullRequest returns the indexed pull request for this snapshot.
func (s Snapshot) PullRequest(repoID, enrichmentID int64, syncedAt time.Time) PullRequest {
	return ReconstructPullRequest(
		0, repoID, s.Number, enrichmentID,
		s.Title, s.URL, s.State, s.Author, s.MergeCommitSHA,
		s.CreatedAt, s.UpdatedAt, s.MergedAt, syncedAt,
	)
}

// Document renders the snapshot as the text that is indexed and returned to
// readers: metadata, description, changed files and the whole discussion.
func (s Snapshot) Document(repoURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%d: %s\n", s.Number, s.Title)
	fmt.Fprintf(&b, "Repository: %s\n", repoURL)
	fmt.Fprintf(&b, "URL: %s\n", s.URL)
	fmt.Fprintf(&b, "State: %s\n", s.State)
	fmt.Fprintf(&b, "Author: %s\n", s.Author)
	fmt.Fprintf(&b, "Created: %s\n", day(s.CreatedAt))
	if !s.MergedAt.IsZero() {
		fmt.Fprintf(&b, "Merged: %s\n", day(s.MergedAt))
	}
	if s.MergeCommitSHA != "" {
		fmt.Fprintf(&b, "Merge commit: %s\n", s.MergeCommitSHA)
	}
	fmt.Fprintf(&b, "Branches: %s <- %s\n", s.BaseRef, s.HeadRef)

	b.WriteString("\n" + descriptionHeading)
	if strings.TrimSpace(s.Body) == "" {
		b.WriteString("(no description)\n")
	} else {
		b.WriteString(strings.TrimSpace(s.Body))
		b.WriteString("\n")
	}

	if s.FileCount > 0 {
		fmt.Fprintf(&b, "\n## Files changed (%d)\n\n", s.FileCount)
		for _, f := range s.Files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		omitted(&b, s.FileCount-len(s.Files), "files")
	}

	if s.CommentCount > 0 {
		b.WriteString("\n## Conversation\n")
		for _, c := range s.Comments {
			comment(&b, c)
		}
		omitted(&b, s.CommentCount-len(s.Comments), "comments")
	}

	reviews := make([]Review, 0, len(s.Reviews))
	for _, r := range s.Reviews {
		if strings.TrimSpace(r.Body) != "" {
			reviews = append(reviews, r)
		}
	}
	if len(reviews) > 0 {
		b.WriteString("\n## Reviews\n")
		for _, r := range reviews {
			fmt.Fprintf(&b, "\n%s (%s, %s):\n%s\n", r.Author, r.State, day(r.SubmittedAt), strings.TrimSpace(r.Body))
		}
	}
	omitted(&b, s.ReviewCount-len(s.Reviews), "reviews")

	if s.ThreadCount > 0 {
		b.WriteString("\n## Review threads\n")
		for _, t := range s.Threads {
			status := "open"
			if t.Resolved {
				status = "resolved"
			}
			location := t.Path
			if t.Line > 0 {
				location = fmt.Sprintf("%s:%d", t.Path, t.Line)
			}
			fmt.Fprintf(&b, "\n### %s (%s)\n", location, status)
			for _, c := range t.Comments {
				comment(&b, c)
			}
			omitted(&b, t.CommentCount-len(t.Comments), "comments")
		}
		omitted(&b, s.ThreadCount-len(s.Threads), "threads")
	}

	return b.String()
}

func comment(b *strings.Builder, c Comment) {
	fmt.Fprintf(b, "\n%s (%s):\n%s\n", c.Author, day(c.CreatedAt), strings.TrimSpace(c.Body))
}

func omitted(b *strings.Builder, n int, what string) {
	if n > 0 {
		fmt.Fprintf(b, "\n(%d more %s not shown)\n", n, what)
	}
}

func day(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("2006-01-02")
}
