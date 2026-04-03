package orchestration

import (
	"context"
	"os"
	"path/filepath"

	"github.com/grovetools/grove-gemini/pkg/gemini"
	"github.com/grovetools/memory/pkg/memory"
)

// FetchRelatedMemories queries the memory DB for semantic matches to the job prompt.
// It returns nil gracefully when the DB is missing, the API key is unavailable,
// or any other error occurs — memory injection is always best-effort.
func FetchRelatedMemories(ctx context.Context, job *Job) []memory.SearchResult {
	if !job.IsMemoryEnabled() || job.PromptBody == "" {
		return nil
	}

	// Test hook to bypass DB and network API calls in E2E tests
	if mock := os.Getenv("GROVE_MOCK_MEMORY_RESULTS"); mock != "" {
		return []memory.SearchResult{
			{Path: "test-memory.md", Content: mock},
		}
	}

	dbPath := os.Getenv("GROVE_MEMORY_DB_PATH")
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dbPath = filepath.Join(home, ".local", "share", "grove", "memory", "memory.db")
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		ulog.Debug("Memory DB not found, skipping memory injection").
			Field("db_path", dbPath).
			Log(ctx)
		return nil
	}

	// Use 3072 dimensions (gemini-embedding-001)
	store, err := memory.Open(dbPath, 3072)
	if err != nil {
		ulog.Warn("Failed to open memory DB").
			Err(err).
			Log(ctx)
		return nil
	}
	defer store.Close()

	client, err := gemini.NewClient(ctx, "")
	if err != nil {
		ulog.Warn("Failed to initialize Gemini client for memory embeddings").
			Err(err).
			Log(ctx)
		return nil
	}

	embedder := memory.NewEmbedder(client, "gemini-embedding-001")

	queryEmbedding, err := embedder.EmbedQuery(ctx, job.PromptBody)
	if err != nil {
		ulog.Warn("Failed to generate embedding for memory search").
			Err(err).
			Log(ctx)
		return nil
	}

	config := memory.DefaultSearchConfig()
	config.Limit = 3

	results, err := store.Search(ctx, job.PromptBody, queryEmbedding, config)
	if err != nil {
		ulog.Warn("Memory search failed").
			Err(err).
			Log(ctx)
		return nil
	}

	if len(results) > 0 {
		ulog.Debug("Found related memories").
			Field("count", len(results)).
			Field("job_id", job.ID).
			Log(ctx)
	}

	return results
}
