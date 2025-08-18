package gemini

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/genai"
)

// Client wraps the Google Generative AI client
type Client struct {
	client *genai.Client
}

// NewClient creates a new Gemini client
func NewClient(ctx context.Context) (*Client, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable not set")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Client{client: client}, nil
}

// GenerateContentWithCache generates content using a cached context and dynamic files
func (c *Client) GenerateContentWithCache(ctx context.Context, model string, prompt string, cacheID string, dynamicFilePaths []string) (string, error) {
	// Upload dynamic files if any
	var requestParts []*genai.Part
	if len(dynamicFilePaths) > 0 {
		fmt.Fprintf(os.Stderr, "\n📤 Uploading dynamic files for request...\n")
		for _, filePath := range dynamicFilePaths {
			// Upload dynamic file
			f, err := uploadFile(ctx, c.client, filePath)
			if err != nil {
				return "", fmt.Errorf("failed to upload dynamic file %s: %w", filePath, err)
			}
			
			// Create part from URI
			part := genai.NewPartFromURI(f.URI, f.MIMEType)
			requestParts = append(requestParts, part)
		}
	}

	// Add the text prompt to requestParts
	if prompt != "" {
		requestParts = append(requestParts, &genai.Part{Text: prompt})
	}
	
	// Create content object with all parts
	userTurn := &genai.Content{
		Role:  genai.RoleUser,
		Parts: requestParts,
	}
	
	// Create contents slice for API
	contentsForAPI := []*genai.Content{userTurn}
	
	// Generate content with optional cache
	var result *genai.GenerateContentResponse
	var err error
	
	startTime := time.Now()
	fmt.Fprintf(os.Stderr, "\n🤖 Generating response...\n")
	
	if cacheID != "" {
		result, err = c.client.Models.GenerateContent(
			ctx,
			model,
			contentsForAPI,
			&genai.GenerateContentConfig{
				CachedContent: cacheID,
			},
		)
	} else {
		// No cache, just dynamic files
		result, err = c.client.Models.GenerateContent(
			ctx,
			model,
			contentsForAPI,
			&genai.GenerateContentConfig{},
		)
	}
	
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %w", err)
	}

	// Calculate duration
	duration := time.Since(startTime)

	// Show token usage
	if result.UsageMetadata != nil {
		fmt.Fprintf(os.Stderr, "\n📊 Token usage:\n")
		if cacheID != "" && result.UsageMetadata.CachedContentTokenCount > 0 {
			fmt.Fprintf(os.Stderr, "  Cached: %d tokens\n", result.UsageMetadata.CachedContentTokenCount)
		}
		fmt.Fprintf(os.Stderr, "  Dynamic + Prompt: %d tokens\n", result.UsageMetadata.PromptTokenCount)
		fmt.Fprintf(os.Stderr, "  Total: %d tokens\n", result.UsageMetadata.TotalTokenCount)
		
		if result.UsageMetadata.CachedContentTokenCount > 0 && result.UsageMetadata.PromptTokenCount > 0 {
			ratio := float64(result.UsageMetadata.CachedContentTokenCount) / 
				float64(result.UsageMetadata.CachedContentTokenCount + result.UsageMetadata.PromptTokenCount) * 100
			fmt.Fprintf(os.Stderr, "  Cache usage: %.1f%%\n", ratio)
		}
		
		fmt.Fprintf(os.Stderr, "  Response time: %.2fs\n", duration.Seconds())
	}

	return result.Text(), nil
}

// GetClient returns the underlying genai client for cache operations
func (c *Client) GetClient() *genai.Client {
	return c.client
}