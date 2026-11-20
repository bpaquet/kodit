package kodit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/helixml/kodit"
	"github.com/helixml/kodit/application/service"
	"github.com/helixml/kodit/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unitTestPollPeriod = 50 * time.Millisecond

func TestNew_RequiresDatabase(t *testing.T) {
	_, err := kodit.New()
	assert.ErrorIs(t, err, kodit.ErrNoDatabase)
}

func TestNew_WithSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() {
		err := client.Close()
		assert.NoError(t, err)
	}()

	// Verify database file was created
	_, err = os.Stat(dbPath)
	assert.NoError(t, err)
}

func TestClient_Close_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)

	// First close should succeed
	err = client.Close()
	assert.NoError(t, err)

	// Second close should return ErrClientClosed
	err = client.Close()
	assert.ErrorIs(t, err, kodit.ErrClientClosed)
}

func TestClient_Repositories_List_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	repos, err := client.Repositories.Find(ctx)
	require.NoError(t, err)
	assert.Empty(t, repos)
}

func TestClient_Tasks_List_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	tasks, err := client.Tasks.List(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClient_Search_ReturnsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	result, err := client.Search.Query(ctx, "test query",
		service.WithLimit(10),
		service.WithSemanticWeight(0.5),
	)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Count())
}

func TestClient_Search_AfterClose_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)

	err = client.Close()
	require.NoError(t, err)

	ctx := context.Background()
	_, err = client.Search.Query(ctx, "test query")
	assert.ErrorIs(t, err, kodit.ErrClientClosed)
}

func TestWithFullPipeline_RequiresTextProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	_, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithFullPipeline(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithFullPipeline requires a text provider")
}

func TestWithRAGPipeline_WorksWithoutTextProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithRAGPipeline(),
		kodit.WithEmbeddingProvider(&stubEmbedder{}),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
}

func TestWithRAGPipeline_WorksWithTextProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Even with a text provider set, RAG pipeline should not require enrichment handlers.
	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithRAGPipeline(),
		kodit.WithEmbeddingProvider(&stubEmbedder{}),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
}

func TestWithDataDir_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "custom_data")
	dbPath := filepath.Join(dataDir, "test.db")

	client, err := kodit.New(
		kodit.WithSQLite(dbPath),
		kodit.WithDataDir(dataDir),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Verify data directory was created
	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func toolNames(client *kodit.Client) map[string]bool {
	names := map[string]bool{}
	for _, tool := range client.MCPServer.Tools() {
		names[tool.Name()] = true
	}
	return names
}

func TestNew_PullRequestsDisabledByDefault(t *testing.T) {
	client, err := kodit.New(
		kodit.WithSQLite(filepath.Join(t.TempDir(), "test.db")),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
	)
	require.NoError(t, err)
	defer func() { assert.NoError(t, client.Close()) }()

	assert.Nil(t, client.PullRequests)
	assert.False(t, toolNames(client)["kodit_pull_request_search"])
}

func TestNew_PullRequestsEnabled(t *testing.T) {
	client, err := kodit.New(
		kodit.WithSQLite(filepath.Join(t.TempDir(), "test.db")),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
		kodit.WithPullRequestsConfig(config.NewPullRequestsConfig().WithEnabled(true)),
	)
	require.NoError(t, err)
	defer func() { assert.NoError(t, client.Close()) }()

	require.NotNil(t, client.PullRequests)
	names := toolNames(client)
	assert.True(t, names["kodit_pull_request_search"])
	assert.True(t, names["kodit_pull_request"])

	hits, err := client.PullRequests.Search(context.Background(), "anything", 10, nil)
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestNew_PullRequestsRejectsNonPositiveFullSyncInterval(t *testing.T) {
	_, err := kodit.New(
		kodit.WithSQLite(filepath.Join(t.TempDir(), "test.db")),
		kodit.WithSkipProviderValidation(),
		kodit.WithWorkerPollPeriod(unitTestPollPeriod),
		kodit.WithPullRequestsConfig(config.NewPullRequestsConfig().WithEnabled(true).WithFullSyncIntervalSeconds(0)),
	)
	assert.Error(t, err)
}
