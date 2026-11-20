// Package github reads pull requests from the GitHub GraphQL API.
package github

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/helixml/kodit/domain/pullrequest"
)

// ErrMissingToken is returned for a GitHub remote URL that carries no token.
var ErrMissingToken = errors.New("github remote URL carries no token: use https://<token>@github.com/<owner>/<repo> or https://<user>:<token>@github.com/<owner>/<repo>")

const githubHost = "github.com"

// Remote is a GitHub repository and the token found in its remote URL.
type Remote struct {
	owner string
	name  string
	token string
}

// ParseRemote extracts the owner, name and token of a GitHub remote URL.
// The token is the password of the URL, or its user name when there is no
// password. A remote on another host returns pullrequest.ErrUnsupportedRemote.
func ParseRemote(remoteURL string) (Remote, error) {
	if strings.HasPrefix(remoteURL, "git@") {
		host, _, _ := strings.Cut(strings.TrimPrefix(remoteURL, "git@"), ":")
		if strings.EqualFold(host, githubHost) {
			return Remote{}, ErrMissingToken
		}
		return Remote{}, pullrequest.ErrUnsupportedRemote
	}

	u, err := url.Parse(remoteURL)
	if err != nil || !strings.EqualFold(u.Hostname(), githubHost) {
		return Remote{}, pullrequest.ErrUnsupportedRemote
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Remote{}, fmt.Errorf("github remote URL path must be /<owner>/<repo>, got %q", u.Path)
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return Remote{}, ErrMissingToken
	}

	token := ""
	if u.User != nil {
		token = u.User.Username()
		if password, ok := u.User.Password(); ok {
			token = password
		}
	}
	if token == "" {
		return Remote{}, ErrMissingToken
	}

	return Remote{
		owner: parts[0],
		name:  strings.TrimSuffix(parts[1], ".git"),
		token: token,
	}, nil
}

// Owner returns the repository owner.
func (r Remote) Owner() string { return r.owner }

// Name returns the repository name.
func (r Remote) Name() string { return r.name }
