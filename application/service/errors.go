package service

import "errors"

// ErrClientClosed indicates the client has been closed.
var ErrClientClosed = errors.New("kodit: client is closed")

// ErrPullRequestNotFound indicates no indexed pull request has the requested
// number.
var ErrPullRequestNotFound = errors.New("pull request not found")
