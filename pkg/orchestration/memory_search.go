package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/grove-gemini/pkg/gemini"
	"github.com/grovetools/memory/pkg/memory"
)

// memoryPrefetchTimeout bounds the ENTIRE memory prefetch (api_key command +
// Gemini client init + EmbedQuery + store search). Offline, the embedding call
// and the api_key command can each stall for tens of seconds with no deadline;
// without this bound the agent launch is delayed with no user-visible progress.
// On expiry the prefetch is abandoned and the agent launches with no memories.
const memoryPrefetchTimeout = 15 * time.Second

// FetchRelatedMemoriesBounded runs FetchRelatedMemories under memoryPrefetchTimeout
// so a slow/offline embedding path can never delay the agent-launch critical
// path past the bound. On timeout it logs a Warn and returns nil so the launch
// proceeds without memories. The passed ctx is still honored for earlier
// cancellation (e.g. job cancellation).
func FetchRelatedMemoriesBounded(ctx context.Context, job *Job) []memory.SearchResult {
	bctx, cancel := context.WithTimeout(ctx, memoryPrefetchTimeout)
	defer cancel()

	results := FetchRelatedMemories(bctx, job)

	if bctx.Err() == context.DeadlineExceeded {
		ulog.Warn("memory prefetch timed out; launching without memories").
			Field("job_id", job.ID).
			Field("timeout", memoryPrefetchTimeout.String()).
			Pretty(theme.IconWarning + " Memory prefetch timed out; launching without memories").
			Log(ctx)
		return nil
	}

	return results
}

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

	embedder := memory.NewGeminiEmbedder(client, "gemini-embedding-001")

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
