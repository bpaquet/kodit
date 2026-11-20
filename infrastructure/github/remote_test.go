package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/helixml/kodit/domain/pullrequest"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		owner string
		repo  string
		token string
	}{
		{name: "token as password", url: "https://x-access-token:tok1@github.com/acme/app.git", owner: "acme", repo: "app", token: "tok1"},
		{name: "token as user", url: "https://tok2@github.com/acme/app", owner: "acme", repo: "app", token: "tok2"},
		{name: "user and token", url: "https://alice:tok3@github.com/acme/app/", owner: "acme", repo: "app", token: "tok3"},
		{name: "host case", url: "https://tok4@GitHub.com/acme/app.git", owner: "acme", repo: "app", token: "tok4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote, err := ParseRemote(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.owner, remote.Owner())
			assert.Equal(t, tt.repo, remote.Name())
			assert.Equal(t, tt.token, remote.token)
		})
	}
}

func TestParseRemote_MissingToken(t *testing.T) {
	for _, url := range []string{
		"https://github.com/acme/app.git",
		"git@github.com:acme/app.git",
		"ssh://git@github.com/acme/app.git",
	} {
		t.Run(url, func(t *testing.T) {
			_, err := ParseRemote(url)
			assert.ErrorIs(t, err, ErrMissingToken)
		})
	}
}

func TestParseRemote_UnsupportedHost(t *testing.T) {
	for _, url := range []string{
		"https://tok@gitlab.com/acme/app.git",
		"git@gitlab.com:acme/app.git",
		"file:///srv/repos/app",
		"/srv/repos/app",
		"https://tok@github.com.evil.example/acme/app.git",
	} {
		t.Run(url, func(t *testing.T) {
			_, err := ParseRemote(url)
			assert.ErrorIs(t, err, pullrequest.ErrUnsupportedRemote)
		})
	}
}

func TestParseRemote_InvalidPath(t *testing.T) {
	_, err := ParseRemote("https://tok@github.com/acme")
	require.Error(t, err)
	assert.NotErrorIs(t, err, pullrequest.ErrUnsupportedRemote)
}
