package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/grovepm/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-core/git"
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
	
	chatRunCmd := &cobra.Command{
		Use:   "run",
		Short: "Run all outstanding chat jobs that are waiting for an LLM response",
		Long: `Scans the configured chat directory for all chats where the last turn is from a user
and executes them sequentially to generate the next LLM response.`,
		RunE: runChatRun,
	}
	
	chatCmd.AddCommand(chatListCmd)
	chatCmd.AddCommand(chatRunCmd)
	return chatCmd
}

// expandChatPath expands home directory and git variables in a path.
func expandChatPath(path string) (string, error) {
	// 1. Expand home directory character '~'.
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get user home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// 2. Expand git-related variables.
	repo, branch, err := git.GetRepoInfo(".")
	if err != nil {
		// Don't fail, just proceed without these variables.
		fmt.Printf("Warning: could not get git info for path expansion: %v\n", err)
	} else {
		// Support both ${VAR} and {{VAR}} patterns
		path = strings.ReplaceAll(path, "${REPO}", repo)
		path = strings.ReplaceAll(path, "${BRANCH}", branch)
		path = strings.ReplaceAll(path, "{{REPO}}", repo)
		path = strings.ReplaceAll(path, "{{BRANCH}}", branch)
	}

	return filepath.Abs(path)
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

	chatDir, err := expandChatPath(flowCfg.ChatDirectory)
	if err != nil {
		return fmt.Errorf("failed to expand chat directory path: %w", err)
	}
	
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
		// Show relative path from chat directory
		relPath, err := filepath.Rel(chatDir, chat.FilePath)
		if err != nil {
			relPath = filepath.Base(chat.FilePath)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", chat.Title, chat.Status, chat.Model, relPath)
	}
	w.Flush()
	return nil
}

func runChatRun(cmd *cobra.Command, args []string) error {
	var runnableChats []*orchestration.Job // Store job objects

	// Check if a specific file was provided
	if len(args) > 0 {
		// Run specific chat file
		chatPath := args[0]
		
		// Verify file exists
		info, err := os.Stat(chatPath)
		if err != nil {
			return fmt.Errorf("chat file not found: %s", chatPath)
		}
		if info.IsDir() {
			return fmt.Errorf("expected a file, got directory: %s", chatPath)
		}
		
		// Load and check if it's a runnable chat
		job, err := orchestration.LoadJob(chatPath)
		if err != nil {
			return fmt.Errorf("failed to load job: %w", err)
		}
		if job.Type != "chat" {
			return fmt.Errorf("file is not a chat job: %s", chatPath)
		}
		
		// Check if it's runnable
		content, err := os.ReadFile(chatPath)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		
		turns, err := orchestration.ParseChatFile(content)
		if err != nil {
			return fmt.Errorf("failed to parse chat: %w", err)
		}
		
		if len(turns) == 0 || turns[len(turns)-1].Speaker != "user" {
			return fmt.Errorf("chat is not runnable (last turn is not from user)")
		}
		
		job.FilePath = chatPath
		runnableChats = append(runnableChats, job)
	} else {
		// Scan chat directory
		flowCfg, err := loadFlowConfig()
		if err != nil {
			return err
		}
		if flowCfg.ChatDirectory == "" {
			return fmt.Errorf("'flow.chat_directory' is not set in your grove.yml configuration")
		}

		chatDir, err := expandChatPath(flowCfg.ChatDirectory)
		if err != nil {
			return fmt.Errorf("failed to expand chat directory path: %w", err)
		}

		// Find all runnable chats by walking the directory
		err = filepath.Walk(chatDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil // Skip directories or inaccessible files
			}
			
			// Look for all .md files
			if !strings.HasSuffix(info.Name(), ".md") {
				return nil
			}

			// First, load the job to ensure it's a chat type
			job, loadErr := orchestration.LoadJob(path)
			if loadErr != nil || job.Type != "chat" {
				return nil // Skip non-chat files or files that fail to load
			}

			// Only check chats that are in pending_user status
			if job.Status != orchestration.JobStatusPendingUser {
				return nil // Skip chats that are completed, failed, etc.
			}

			// Read the raw content to check the last turn
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil // Skip unreadable files
			}

			// With the new parser, we can reliably check the last turn
			turns, parseErr := orchestration.ParseChatFile(content)
			if parseErr == nil && len(turns) > 0 {
				// A chat is runnable if the last turn was from the user
				if turns[len(turns)-1].Speaker == "user" {
					// Keep the original file path for execution
					job.FilePath = path
					runnableChats = append(runnableChats, job)
				}
			}
			return nil
		})

		if err != nil {
			return fmt.Errorf("failed to scan for runnable chats in %s: %w", chatDir, err)
		}
	}

	if len(runnableChats) == 0 {
		fmt.Println("No runnable chats found.")
		return nil
	}

	fmt.Printf("Found %d runnable chat(s). Executing sequentially...\n\n", len(runnableChats))

	var executionErrors []error
	for _, job := range runnableChats {
		fmt.Printf("--- Running Chat: %s ---\n", job.Title)

		// Execute the job using os/exec to call flow plan run
		// This avoids recursion issues and correctly uses the command-line interface.
		flowBinary := os.Args[0] // Get the path to the current flow binary
		// Pass the FILE path directly to flow plan run, as per our updated design
		runCmd := exec.Command(flowBinary, "plan", "run", job.FilePath, "--yes")
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		runCmd.Stdin = os.Stdin

		if err := runCmd.Run(); err != nil {
			errorMsg := fmt.Sprintf("✗ Error running chat '%s': %v\n", job.Title, err)
			fmt.Print(errorMsg)
			executionErrors = append(executionErrors, fmt.Errorf("%s", errorMsg))
		}
		fmt.Printf("--- Finished Chat: %s ---\n\n", job.Title)
	}

	if len(executionErrors) > 0 {
		return fmt.Errorf("%d chat(s) failed to execute", len(executionErrors))
	}

	fmt.Println("All runnable chats processed successfully.")
	return nil
}