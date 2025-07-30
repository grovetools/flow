package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/grovepm/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var (
	chatSpecFile string
	chatTitle    string
	chatModel    string
	chatStatus   string
)

func GetChatCommand() *cobra.Command {
	chatCmd := &cobra.Command{
		Use:   "chat",
		Short: "Start or manage a chat-based job from a file",
		Long: `Initializes a markdown file as a runnable chat job by adding the necessary frontmatter.

Example:
  flow chat -s /path/to/my-notes/new-feature.md`,
		RunE: runChatInit,
	}

	chatCmd.Flags().StringVarP(&chatSpecFile, "spec-file", "s", "", "Path to an existing markdown file to convert into a chat job (required)")
	chatCmd.Flags().StringVarP(&chatTitle, "title", "t", "", "Title for the chat job (defaults to the filename)")
	chatCmd.Flags().StringVarP(&chatModel, "model", "m", "", "LLM model to use for the chat (defaults to flow.oneshot_model from config)")
	chatCmd.MarkFlagRequired("spec-file")

	chatListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all chat jobs in the configured chat directory",
		RunE:  runChatList,
	}
	chatListCmd.Flags().StringVar(&chatStatus, "status", "", "Filter chats by status (e.g., pending_user, completed, running)")
	
	chatCmd.AddCommand(chatListCmd)
	return chatCmd
}

func runChatInit(cmd *cobra.Command, args []string) error {
	filePath := chatSpecFile
	
	// Check if path exists
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", filePath)
		}
		return fmt.Errorf("failed to stat file: %s: %w", filePath, err)
	}
	
	// Check if it's a directory
	if info.IsDir() {
		return fmt.Errorf("path is a directory, not a file: %s", filePath)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	frontmatter, body, _ := orchestration.ParseFrontmatter(content)
	if frontmatter == nil {
		frontmatter = make(map[string]interface{})
		body = content // If parsing fails, treat all content as body
	}
	
	// Check if already initialized as a chat
	if jobType, ok := frontmatter["type"].(string); ok && jobType == "chat" {
		return fmt.Errorf("file %s is already initialized as a chat job", filePath)
	}

	// --- Populate Frontmatter ---
	if _, ok := frontmatter["id"]; !ok {
		frontmatter["id"] = "job-" + uuid.New().String()[:8]
	}
	if chatTitle != "" {
		frontmatter["title"] = chatTitle
	} else if _, ok := frontmatter["title"]; !ok {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		frontmatter["title"] = strings.TrimSuffix(base, ext)
	}

	frontmatter["type"] = "chat"
	
	// Set model - use flag value, or config default, or fallback
	model := chatModel
	if model == "" {
		flowCfg, _ := loadFlowConfig()
		if flowCfg != nil && flowCfg.OneshotModel != "" {
			model = flowCfg.OneshotModel
		} else {
			model = "gemini-2.5-pro" // Final fallback
		}
	}
	frontmatter["model"] = model
	
	frontmatter["status"] = "pending_user" // Start as pending_user, waiting for the first run command
	frontmatter["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	
	// Ensure aliases and tags are present
	if _, ok := frontmatter["aliases"]; !ok {
		frontmatter["aliases"] = []interface{}{}
	}
	if _, ok := frontmatter["tags"]; !ok {
		frontmatter["tags"] = []interface{}{}
	}

	newContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
	if err != nil {
		return fmt.Errorf("failed to build new file content: %w", err)
	}

	if err := os.WriteFile(filePath, newContent, 0644); err != nil {
		return fmt.Errorf("failed to write updated file: %w", err)
	}

	fmt.Printf("✓ Initialized chat job: %s\n", filePath)
	fmt.Printf("  You can now start the conversation with: flow plan run %s\n", filePath)
	return nil
}

func runChatList(cmd *cobra.Command, args []string) error {
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}
	if flowCfg.ChatDirectory == "" {
		return fmt.Errorf("'flow.chat_directory' is not set in your grove.yml configuration")
	}

	chatDir := flowCfg.ChatDirectory
	
	// Recursively find all .md files in the chat directory
	var chats []*orchestration.Job
	err = filepath.Walk(chatDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}
		
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			job, err := orchestration.LoadJob(path)
			if err == nil && job.Type == "chat" {
				if chatStatus == "" || string(job.Status) == chatStatus {
					chats = append(chats, job)
				}
			}
		}
		return nil
	})
	
	if err != nil {
		return fmt.Errorf("failed to walk chat directory %s: %w", chatDir, err)
	}

	if len(chats) == 0 {
		fmt.Println("No chat jobs found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "TITLE\tSTATUS\tMODEL\tFILE")
	for _, chat := range chats {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", chat.Title, chat.Status, chat.Model, filepath.Base(chat.FilePath))
	}
	w.Flush()
	return nil
}