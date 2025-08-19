package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var (
	chatSpecFile   string
	chatTitle      string
	chatModel      string
	chatStatus     string
	chatLaunchHost bool
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
		Use:   "run [title...]",
		Short: "Run outstanding chat jobs that are waiting for an LLM response",
		Long: `Scans the configured chat directory for chats where the last turn is from a user
and executes them sequentially to generate the next LLM response.

You can optionally specify chat titles to run only specific chats:
  flow chat run                     # Run all pending chats
  flow chat run testing-situation   # Run only the chat titled "testing-situation"
  flow chat run chat1 chat2         # Run multiple specific chats`,
		RunE: runChatRun,
	}

	chatLaunchCmd := &cobra.Command{
		Use:   "launch [title-or-file]",
		Short: "Launch an interactive agent session from a chat file",
		Long: `Launches a chat in a new detached tmux session, pre-filling the agent prompt with the chat content.
This allows you to quickly jump from an idea in a markdown file into an interactive session.

Example:
  flow chat launch issue123             # Launch by title (searches in chat directory)
  flow chat launch /path/to/issue.md    # Launch by file path`,
		Args: cobra.MaximumNArgs(1),
		RunE: runChatLaunch,
	}
	chatLaunchCmd.Flags().BoolVar(&chatLaunchHost, "host", false, "Launch agent on the host in the main git repo, not in a container worktree")

	chatCmd.AddCommand(chatListCmd)
	chatCmd.AddCommand(chatRunCmd)
	chatCmd.AddCommand(chatLaunchCmd)
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

// ensureChatJob ensures a file is initialized as a chat job, initializing it if necessary
func ensureChatJob(filePath string) (*orchestration.Job, error) {
	// Try to load as a job first
	job, err := orchestration.LoadJob(filePath)
	if err == nil && job.Type == "chat" {
		// Already a valid chat job
		return job, nil
	}

	// If it's not a job (or not a chat), initialize it
	var notAJob orchestration.ErrNotAJob
	if err != nil && !errors.As(err, &notAJob) {
		// Some other error occurred
		return nil, fmt.Errorf("failed to load job: %w", err)
	}

	// Read the file content
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	// Parse existing frontmatter if any
	frontmatter, body, _ := orchestration.ParseFrontmatter(content)
	if frontmatter == nil {
		frontmatter = make(map[string]interface{})
		body = content // If parsing fails, treat all content as body
	}

	// Check if it's already a different type of job
	if jobType, ok := frontmatter["type"].(string); ok && jobType != "" && jobType != "chat" {
		return nil, fmt.Errorf("file %s is already initialized as a %s job, not a chat", filePath, jobType)
	}

	// Initialize as chat job
	if _, ok := frontmatter["id"]; !ok {
		frontmatter["id"] = "job-" + uuid.New().String()[:8]
	}
	if _, ok := frontmatter["title"]; !ok {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		frontmatter["title"] = strings.TrimSuffix(base, ext)
	}

	frontmatter["type"] = "chat"

	// Set model - use config default or fallback
	flowCfg, _ := loadFlowConfig()
	model := "gemini-2.5-pro" // Default fallback
	if flowCfg != nil && flowCfg.OneshotModel != "" {
		model = flowCfg.OneshotModel
	}
	frontmatter["model"] = model

	frontmatter["status"] = "pending_user"
	frontmatter["updated_at"] = time.Now().UTC().Format(time.RFC3339)

	// Ensure aliases and tags are present
	if _, ok := frontmatter["aliases"]; !ok {
		frontmatter["aliases"] = []interface{}{}
	}
	if _, ok := frontmatter["tags"]; !ok {
		frontmatter["tags"] = []interface{}{}
	}

	// Rebuild the file with frontmatter
	newContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
	if err != nil {
		return nil, fmt.Errorf("failed to build new file content: %w", err)
	}

	// Write the updated file
	if err := os.WriteFile(filePath, newContent, 0644); err != nil {
		return nil, fmt.Errorf("failed to write updated file: %w", err)
	}

	fmt.Printf("✓ Initialized chat job: %s\n", filePath)

	// Load and return the newly created job
	return orchestration.LoadJob(filePath)
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
	fmt.Printf("  You can now start the conversation with: flow chat run %s\n", filePath)
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

	// Check if JSON output is requested
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		// Output as JSON array
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(chats)
	}

	// Table output
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
	var titleFilter map[string]bool

	// Check if specific titles were provided
	if len(args) > 0 {
		// Check if the first argument looks like a file path
		if strings.Contains(args[0], "/") || strings.HasSuffix(args[0], ".md") {
			// Legacy behavior: Run specific chat file
			chatPath := args[0]

			// Verify file exists
			info, err := os.Stat(chatPath)
			if err != nil {
				return fmt.Errorf("chat file not found: %s", chatPath)
			}
			if info.IsDir() {
				return fmt.Errorf("expected a file, got directory: %s", chatPath)
			}

			// Use ensureChatJob to load or initialize the chat
			job, err := ensureChatJob(chatPath)
			if err != nil {
				return fmt.Errorf("failed to ensure chat job: %w", err)
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

			// Debug output
			if v, _ := cmd.Flags().GetBool("verbose"); v {
				fmt.Printf("DEBUG: Found %d turns\n", len(turns))
				for i, turn := range turns {
					fmt.Printf("DEBUG: Turn %d - Speaker: %s, Content preview: %.50s...\n", i+1, turn.Speaker, strings.ReplaceAll(turn.Content, "\n", " "))
				}
				if len(turns) > 0 {
					lastTurn := turns[len(turns)-1]
					fmt.Printf("DEBUG: Last turn - Speaker: %s, Has directive: %v\n", lastTurn.Speaker, lastTurn.Directive != nil)
					if lastTurn.Directive != nil {
						fmt.Printf("DEBUG: Last turn directive - Template: %s, ID: %s\n", lastTurn.Directive.Template, lastTurn.Directive.ID)
					}
				}
			}

			if len(turns) == 0 || turns[len(turns)-1].Speaker != "user" {
				return fmt.Errorf("chat is not runnable (last turn is not from user)")
			}

			job.FilePath = chatPath
			runnableChats = append(runnableChats, job)
		} else {
			// New behavior: Filter by titles
			titleFilter = make(map[string]bool)
			for _, title := range args {
				titleFilter[title] = true
			}
		}
	}

	// If we don't have specific file paths, scan the chat directory
	if len(runnableChats) == 0 {
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

			// If title filter is active, check if this chat's title matches
			if titleFilter != nil && !titleFilter[job.Title] {
				return nil // Skip chats not in the filter
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
		if titleFilter != nil {
			// User specified titles but none were found
			fmt.Printf("No runnable chats found with the specified title(s): %s\n", strings.Join(args, ", "))
			fmt.Println("\nAvailable chats:")
			// Show available chats by running list logic
			flowCfg, _ := loadFlowConfig()
			if flowCfg != nil && flowCfg.ChatDirectory != "" {
				chatDir, _ := expandChatPath(flowCfg.ChatDirectory)
				filepath.Walk(chatDir, func(path string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
						return nil
					}
					if job, err := orchestration.LoadJob(path); err == nil && job.Type == "chat" {
						fmt.Printf("  - %s (status: %s)\n", job.Title, job.Status)
					}
					return nil
				})
			}
		} else {
			fmt.Println("No runnable chats found.")
		}
		return nil
	}

	fmt.Printf("Found %d runnable chat(s). Executing sequentially...\n\n", len(runnableChats))

	// Load flow config for orchestrator
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}

	// Create orchestrator config
	orchConfig := &orchestration.OrchestratorConfig{
		MaxParallelJobs:     1, // Chat jobs run sequentially
		CheckInterval:       5 * time.Second,
		ModelOverride:       "", // Use job's model
		MaxConsecutiveSteps: 20,
	}

	// Create Docker client for the orchestrator
	dockerClient, err := docker.NewSDKClient()
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}

	var executionErrors []error
	for _, job := range runnableChats {
		fmt.Printf("--- Running Chat: %s ---\n", job.Title)

		// Create a minimal plan for this chat job
		plan := &orchestration.Plan{
			Directory: filepath.Dir(job.FilePath),
			Jobs:      []*orchestration.Job{job},
			Orchestration: &orchestration.Config{
				OneshotModel:         flowCfg.OneshotModel,
				TargetAgentContainer: flowCfg.TargetAgentContainer,
				PlansDirectory:       flowCfg.PlansDirectory,
				MaxConsecutiveSteps:  flowCfg.MaxConsecutiveSteps,
			},
		}

		// Create orchestrator
		orch, err := orchestration.NewOrchestrator(plan, orchConfig, dockerClient)
		if err != nil {
			errorMsg := fmt.Sprintf("✗ Error creating orchestrator for chat '%s': %v\n", job.Title, err)
			fmt.Print(errorMsg)
			executionErrors = append(executionErrors, fmt.Errorf("%s", errorMsg))
			continue
		}

		// Use the orchestrator to run the specific job
		ctx := context.Background()
		if err := orch.RunJob(ctx, job.FilePath); err != nil {
			errorMsg := fmt.Sprintf("✗ Error running chat '%s': %v\n", job.Title, err)
			fmt.Print(errorMsg)
			executionErrors = append(executionErrors, fmt.Errorf("%s", errorMsg))
		}
		fmt.Printf("--- Finished Chat: %s ---\n\n", job.Title)
	}

	// Wait for any pending hooks to complete
	orchestration.WaitForHooks()

	if len(executionErrors) > 0 {
		return fmt.Errorf("%d chat(s) failed to execute", len(executionErrors))
	}

	fmt.Println("All runnable chats processed successfully.")
	return nil
}

// resolveChatPath finds the chat file from a title or file path argument
func resolveChatPath(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("please specify a chat title or file path")
	}

	arg := args[0]

	// Check if it looks like a file path
	if strings.Contains(arg, "/") || strings.HasSuffix(arg, ".md") {
		// Direct file path - verify it exists
		info, err := os.Stat(arg)
		if err != nil {
			return "", fmt.Errorf("chat file not found: %s", arg)
		}
		if info.IsDir() {
			return "", fmt.Errorf("expected a file, got directory: %s", arg)
		}
		return arg, nil
	}

	// Otherwise, it's a title - search for it
	chatTitle := arg
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return "", err
	}
	if flowCfg.ChatDirectory == "" {
		return "", fmt.Errorf("'flow.chat_directory' is not set in your grove.yml configuration")
	}

	chatDir, err := expandChatPath(flowCfg.ChatDirectory)
	if err != nil {
		return "", fmt.Errorf("failed to expand chat directory path: %w", err)
	}

	// Search for the chat by title
	var chatPath string
	var found bool
	err = filepath.Walk(chatDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		job, err := orchestration.LoadJob(path)
		if err == nil && job.Type == "chat" && job.Title == chatTitle {
			chatPath = path
			found = true
			return filepath.SkipDir // Stop searching
		}
		return nil
	})

	if err != nil && err != filepath.SkipDir {
		return "", fmt.Errorf("failed to search chat directory: %w", err)
	}

	if !found {
		return "", fmt.Errorf("chat not found with title: %s", chatTitle)
	}

	return chatPath, nil
}

func runChatLaunch(cmd *cobra.Command, args []string) error {
	// Check if --host flag was used
	if chatLaunchHost {
		return runChatLaunchHost(cmd, args)
	}

	// Resolve the chat file path
	chatPath, err := resolveChatPath(args)
	if err != nil {
		return err
	}

	// We no longer need to read the full content here since we're passing the file path
	// Just verify the file is readable
	if _, err := os.Stat(chatPath); err != nil {
		return fmt.Errorf("failed to access chat file: %w", err)
	}

	// Load configuration
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}
	container := flowCfg.TargetAgentContainer
	if container == "" {
		return fmt.Errorf("'flow.target_agent_container' is not set in your grove.yml")
	}

	// Pre-flight check: verify container is running (unless skipped for testing)
	ctx := cmd.Context()
	if !shouldSkipDockerCheck() {
		dockerClient, err := docker.NewSDKClient()
		if err != nil {
			return fmt.Errorf("failed to create docker client: %w", err)
		}

		if !dockerClient.IsContainerRunning(ctx, container) {
			return fmt.Errorf("container '%s' is not running. Did you run 'grove-proxy up'?", container)
		}
	}

	// Load full config to get agent args
	fullCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get git root
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	// If UseSuperprojectRoot is enabled, get the superproject root
	if fullCfg.Agent.UseSuperprojectRoot {
		superRoot, err := git.GetSuperprojectRoot(gitRoot)
		if err == nil && superRoot != "" {
			gitRoot = superRoot
		}
	}

	// Load the job to get the title from frontmatter
	job, err := orchestration.LoadJob(chatPath)
	if err != nil {
		return fmt.Errorf("failed to load chat job: %w", err)
	}

	// Prioritize worktree from frontmatter, fall back to deriving from filename
	var worktreeName string
	if job.Worktree != "" {
		worktreeName = job.Worktree
	} else {
		worktreeName = deriveWorktreeName(chatPath)
	}

	// Prepare the worktree at the git root
	wm := git.NewWorktreeManager()
	worktreePath, err := wm.GetOrPrepareWorktree(ctx, gitRoot, worktreeName, "interactive")
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}

	// Configure Canopy hooks for the worktree
	if err := configureCanopyHooks(worktreePath); err != nil {
		return fmt.Errorf("failed to configure canopy hooks: %w", err)
	}

	// Debug: Log config status
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Agent.MountWorkspaceAtHostPath = %v\n", fullCfg.Agent.MountWorkspaceAtHostPath)
	}

	// Build the agent command with the chat file path
	agentCommand := buildAgentCommandFromChat(chatPath, fullCfg.Agent.Args)

	// Prepare launch parameters
	repoName := filepath.Base(gitRoot)
	// Use the title from frontmatter for the session name
	sessionTitle := sanitizeForTmuxSession(job.Title)
	sessionName := fmt.Sprintf("%s__%s", repoName, sessionTitle)

	params := launchParameters{
		sessionName:      sessionName,
		container:        container,
		hostWorktreePath: worktreePath,
		agentCommand:     agentCommand,
	}

	// Calculate container work directory
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to calculate relative path: %w", err)
	}
	if fullCfg.Agent.MountWorkspaceAtHostPath {
		params.containerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		params.containerWorkDir = filepath.Join("/workspace", repoName, relPath)
	}

	// Launch the session using the same logic as plan launch
	executor := &exec.RealCommandExecutor{}
	return launchTmuxSession(executor, params)
}

// deriveWorktreeName creates a valid worktree name from a file path or title
func deriveWorktreeName(chatPath string) string {
	base := filepath.Base(chatPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	// Sanitize the name to be a valid worktree name
	// Replace spaces and special characters with hyphens
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)

	// Remove consecutive hyphens
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}

	// Trim hyphens from start and end
	name = strings.Trim(name, "-")

	// Ensure it's not empty
	if name == "" {
		name = "chat-" + uuid.New().String()[:8]
	}

	// Ensure it's not too long (max 100 chars)
	if len(name) > 100 {
		name = name[:100]
	}

	return name
}

// buildAgentCommandFromChat creates the agent command from chat content
func buildAgentCommandFromChat(chatPath string, agentArgs []string) string {
	// Instead of passing the entire content, instruct claude to read the file
	instruction := fmt.Sprintf("Read the file %s and respond to the latest user message in the conversation", chatPath)
	escapedInstruction := "'" + strings.ReplaceAll(instruction, "'", "'\\''") + "'"

	// Build command with args
	cmdParts := []string{"claude"}
	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedInstruction)

	return strings.Join(cmdParts, " ")
}

// runChatLaunchHost launches a chat in host mode (without container/worktree)
func runChatLaunchHost(cmd *cobra.Command, args []string) error {
	// 1. Resolve Chat Path
	chatPath, err := resolveChatPath(args)
	if err != nil {
		return err
	}
	absChatPath, err := filepath.Abs(chatPath)
	if err != nil {
		return fmt.Errorf("could not get absolute path for chat file: %w", err)
	}

	// 2. Determine Git Root & Session Name
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}
	repoName := filepath.Base(gitRoot)
	sessionName := sanitizeForTmuxSession(repoName)

	executor := &exec.RealCommandExecutor{}

	// 3. Ensure Tmux Session Exists
	err = executor.Execute("tmux", "has-session", "-t", sessionName)
	if err != nil { // has-session returns error if session doesn't exist
		fmt.Printf("✓ Tmux session '%s' not found, creating it...\n", sessionName)
		if createErr := executor.Execute("tmux", "new-session", "-d", "-s", sessionName, "-c", gitRoot); createErr != nil {
			return fmt.Errorf("failed to create tmux session '%s': %w", sessionName, createErr)
		}
	}

	// 4. Create New Window
	chatFileName := strings.TrimSuffix(filepath.Base(chatPath), filepath.Ext(chatPath))
	windowName := "chat-" + sanitizeForTmuxSession(chatFileName)

	// Create the window and set its working directory to the git root
	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", gitRoot); err != nil {
		return fmt.Errorf("failed to create new tmux window: %w", err)
	}

	// 5. Build and Send Agent Command
	fullCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	// Use the absolute path for the chat file
	agentCommand := buildAgentCommandFromChat(absChatPath, fullCfg.Agent.Args)

	// Target the new window precisely
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		return fmt.Errorf("failed to send command to tmux: %w", err)
	}

	// 6. Provide User Feedback
	fmt.Printf("✓ Launched chat in new window '%s' within session '%s'.\n", windowName, sessionName)
	fmt.Printf("  Attach with: tmux attach -t %s\n", sessionName)
	return nil
}

// sanitizeForTmuxSession creates a valid tmux session name from a title
func sanitizeForTmuxSession(title string) string {
	// Replace spaces and special characters with hyphens
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, title)

	// Convert to lowercase for consistency
	sanitized = strings.ToLower(sanitized)

	// Remove consecutive hyphens
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}

	// Trim hyphens from start and end
	sanitized = strings.Trim(sanitized, "-")

	// Ensure it's not empty
	if sanitized == "" {
		sanitized = "session"
	}

	// Tmux session names should not be too long
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}

	return sanitized
}
