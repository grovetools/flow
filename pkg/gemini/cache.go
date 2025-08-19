package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/genai"
)

// CacheInfo stores information about cached files
type CacheInfo struct {
	CacheID          string            `json:"cache_id"`
	CacheName        string            `json:"cache_name"`
	CachedFileHashes map[string]string `json:"cached_file_hashes"`
	Model            string            `json:"model"`
	CreatedAt        time.Time         `json:"created_at"`
	ExpiresAt        time.Time         `json:"expires_at"`
}

// CacheManager manages the cache lifecycle for Gemini API
type CacheManager struct {
	workingDir string
	cacheDir   string
}

// NewCacheManager creates a new cache manager
func NewCacheManager(workingDir string) *CacheManager {
	cacheDir := filepath.Join(workingDir, ".grove", "gemini-cache")
	return &CacheManager{
		workingDir: workingDir,
		cacheDir:   cacheDir,
	}
}

// GetOrCreateCache returns an existing valid cache or creates a new one
func (m *CacheManager) GetOrCreateCache(ctx context.Context, client *Client, model string, coldContextFilePath string, ttl time.Duration) (*CacheInfo, error) {
	// Check if the cold context file exists
	if _, err := os.Stat(coldContextFilePath); err != nil {
		if os.IsNotExist(err) {
			// No cold context file, return nil (no cache to use)
			return nil, nil
		}
		return nil, fmt.Errorf("checking cold context file: %w", err)
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(m.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	// Generate cache key based on the cold context file path
	cacheKey := generateCacheKey([]string{coldContextFilePath})
	cacheInfoFile := filepath.Join(m.cacheDir, "hybrid_"+cacheKey+".json")

	// Try to load existing cache info
	var cacheInfo CacheInfo
	needNewCache := false

	if data, err := os.ReadFile(cacheInfoFile); err == nil {
		if err := json.Unmarshal(data, &cacheInfo); err == nil {
			fmt.Fprintf(os.Stderr, "📁 Found existing cache info\n")
			
			// Check if cache expired
			if time.Now().After(cacheInfo.ExpiresAt) {
				fmt.Fprintf(os.Stderr, "⏰ Cache expired at %s\n", cacheInfo.ExpiresAt.Format(time.RFC3339))
				needNewCache = true
			} else if changed, changedFiles := hasFilesChanged(cacheInfo.CachedFileHashes, []string{coldContextFilePath}); changed {
				fmt.Fprintf(os.Stderr, "🔄 Cached files have changed:\n")
				for _, file := range changedFiles {
					fmt.Fprintf(os.Stderr, "   • %s\n", file)
				}
				needNewCache = true
			} else {
				fmt.Fprintf(os.Stderr, "✅ Cache is valid until %s\n", cacheInfo.ExpiresAt.Format(time.RFC3339))
				return &cacheInfo, nil
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "🆕 No existing cache found\n")
		needNewCache = true
	}

	// Create new cache if needed
	if needNewCache {
		// First, check if the file is large enough for caching
		content, err := os.ReadFile(coldContextFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", coldContextFilePath, err)
		}
		
		estimatedTokens := estimateTokens(content)
		minTokensForCache := 4096
		
		if estimatedTokens < minTokensForCache {
			fmt.Fprintf(os.Stderr, "\n⚠️  Cached context is too small for Gemini caching\n")
			fmt.Fprintf(os.Stderr, "   Estimated tokens: %d (minimum required: %d)\n", estimatedTokens, minTokensForCache)
			fmt.Fprintf(os.Stderr, "   Suggestion: Move all content to hot context (.grove/context) for better performance\n")
			fmt.Fprintf(os.Stderr, "   Proceeding without cache...\n")
			return nil, nil // Return nil to indicate no cache should be used
		}
		
		fmt.Fprintf(os.Stderr, "\n📤 Uploading files for cache...\n")
		fmt.Fprintf(os.Stderr, "   Estimated tokens: %d\n", estimatedTokens)
		
		fileHashes := make(map[string]string)
		var parts []*genai.Part
		
		// Calculate hash
		hashArray := sha256.Sum256(content)
		hash := hex.EncodeToString(hashArray[:])
		fileHashes[coldContextFilePath] = hash
		
		// Upload file
		f, err := uploadFile(ctx, client.GetClient(), coldContextFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to upload %s: %w", coldContextFilePath, err)
		}
		parts = append(parts, genai.NewPartFromURI(f.URI, f.MIMEType))

		// Create cache
		fmt.Fprintf(os.Stderr, "\n🔨 Creating cache...\n")
		contents := []*genai.Content{
			genai.NewContentFromParts(parts, genai.RoleUser),
		}

		cacheConfig := &genai.CreateCachedContentConfig{
			Contents: contents,
			TTL:      ttl,
		}

		cache, err := client.GetClient().Caches.Create(ctx, model, cacheConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create cache: %w", err)
		}

		// Save cache info
		cacheInfo = CacheInfo{
			CacheID:          cache.Name,
			CacheName:        cacheKey,
			CachedFileHashes: fileHashes,
			Model:            model,
			CreatedAt:        time.Now(),
			ExpiresAt:        cache.ExpireTime,
		}

		data, _ := json.MarshalIndent(cacheInfo, "", "  ")
		if err := os.WriteFile(cacheInfoFile, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to save cache info: %w", err)
		}
		
		fmt.Fprintf(os.Stderr, "  ✅ Cache created: %s\n", cache.Name)
		fmt.Fprintf(os.Stderr, "  📅 Expires: %s\n", cache.ExpireTime.Format(time.RFC3339))
	}

	return &cacheInfo, nil
}

// hashFile calculates SHA256 hash of a file
func hashFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:]), nil
}

// generateCacheKey creates a unique key for a set of files
func generateCacheKey(files []string) string {
	h := sha256.New()
	h.Write([]byte("hybrid_v1"))
	for _, f := range files {
		h.Write([]byte(filepath.Clean(f)))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// estimateTokens provides a rough estimate of token count for a file
// Using a simple heuristic: ~1 token per 4 characters (common for code/text)
func estimateTokens(content []byte) int {
	return len(content) / 4
}

// hasFilesChanged checks if any files have changed and returns the changed files
func hasFilesChanged(oldHashes map[string]string, files []string) (bool, []string) {
	var changedFiles []string
	
	for _, file := range files {
		newHash, err := hashFile(file)
		if err != nil {
			changedFiles = append(changedFiles, fmt.Sprintf("%s (error reading file: %v)", file, err))
			continue
		}
		
		if oldHash, exists := oldHashes[file]; !exists {
			changedFiles = append(changedFiles, fmt.Sprintf("%s (new file)", file))
		} else if oldHash != newHash {
			changedFiles = append(changedFiles, file)
		}
	}
	
	return len(changedFiles) > 0, changedFiles
}