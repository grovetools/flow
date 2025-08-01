package cmd

import (
	"errors"
	"strings"
	"testing"
	
	"github.com/grovepm/grove-flow/pkg/exec"
	"github.com/grovepm/grove-flow/pkg/orchestration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAgentCommand(t *testing.T) {
	plan := &orchestration.Plan{
		Directory: "/test/plan",
	}
	
	tests := []struct {
		name        string
		job         *orchestration.Job
		worktreePath string
		agentArgs   []string
		expectedCmd string
		wantErr     bool
	}{
		{
			name: "Simple prompt",
			job: &orchestration.Job{
				PromptBody: "Hello world",
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{},
			expectedCmd:  "claude 'Hello world'",
		},
		{
			name: "Prompt with single quote",
			job: &orchestration.Job{
				PromptBody: "It's a test",
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{},
			expectedCmd:  "claude 'It'\\''s a test'",
		},
		{
			name: "Prompt with source files",
			job: &orchestration.Job{
				PromptBody:   "Implement this feature.",
				PromptSource: []string{"src/main.go", "design.md"},
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{},
			expectedCmd:  "claude 'Implement this feature.\n\nRelevant files for context:\n- src/main.go\n- design.md\n'",
		},
		{
			name: "Empty prompt",
			job: &orchestration.Job{
				PromptBody: "",
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{},
			expectedCmd:  "claude ''",
		},
		{
			name: "Prompt with multiple single quotes",
			job: &orchestration.Job{
				PromptBody: "It's Bob's test and it's working",
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{},
			expectedCmd:  "claude 'It'\\''s Bob'\\''s test and it'\\''s working'",
		},
		{
			name: "Prompt with agent args",
			job: &orchestration.Job{
				PromptBody: "Hello world",
			},
			worktreePath: "/test/worktree",
			agentArgs:    []string{"--dangerously-skip-permissions", "--verbose"},
			expectedCmd:  "claude --dangerously-skip-permissions --verbose 'Hello world'",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := buildAgentCommand(tt.job, plan, tt.worktreePath, tt.agentArgs)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedCmd, cmd)
			}
		})
	}
}

func TestLaunchTmuxSession(t *testing.T) {
	tests := []struct {
		name          string
		params        launchParameters
		setupMock     func(*exec.MockCommandExecutor)
		expectedCmds  []string
		expectError   bool
		errorContains string
	}{
		{
			name: "Happy path",
			params: launchParameters{
				sessionName:      "grove-test-job",
				container:        "my-agent",
				hostWorktreePath: "/path/to/repo/.grove-worktrees/my-worktree",
				containerWorkDir: "/workspace/repo/.grove-worktrees/my-worktree",
				agentCommand:     "claude 'do the thing'",
			},
			setupMock: func(m *exec.MockCommandExecutor) {
				// Default behavior - all commands succeed
			},
			expectedCmds: []string{
				// 1. Creates the session with window 1 (host shell) in the correct host path
				"tmux new-session -d -s grove-test-job -c /path/to/repo/.grove-worktrees/my-worktree",
				// 2. Creates window 2 (agent shell) inside the container at the correct container path
				"tmux new-window -t grove-test-job -n agent docker exec -it -w /workspace/repo/.grove-worktrees/my-worktree my-agent sh",
				// 3. Sends the agent command to the new agent window and executes it
				"tmux send-keys -t grove-test-job:agent claude 'do the thing' C-m",
				// 4. Switches focus back to the host shell window for the user
				"tmux select-window -t grove-test-job:1",
			},
			expectError: false,
		},
		{
			name: "Tmux not found",
			params: launchParameters{
				sessionName:  "grove-test-job",
				container:    "my-agent",
				agentCommand: "claude 'test'",
			},
			setupMock: func(m *exec.MockCommandExecutor) {
				m.LookPathFunc = func(file string) (string, error) {
					if file == "tmux" {
						return "", errors.New("not found")
					}
					return "/path/to/" + file, nil
				}
			},
			expectedCmds:  []string{},
			expectError:   true,
			errorContains: "tmux command not found",
		},
		{
			name: "Session already exists",
			params: launchParameters{
				sessionName:      "grove-test-job",
				container:        "my-agent",
				hostWorktreePath: "/path/to/repo/.grove-worktrees/test",
				containerWorkDir: "/workspace/test",
				agentCommand:     "claude 'test'",
			},
			setupMock: func(m *exec.MockCommandExecutor) {
				m.ExecuteFunc = func(name string, arg ...string) error {
					if name == "tmux" && len(arg) > 0 && arg[0] == "new-session" {
						return &exec.ExecError{
							Err:    errors.New("exit status 1"),
							Output: "duplicate session: grove-test-job",
						}
					}
					return nil
				}
			},
			expectedCmds: []string{
				"tmux new-session -d -s grove-test-job -c /path/to/repo/.grove-worktrees/test",
			},
			expectError: false, // This is not a fatal error
		},
		{
			name: "Failed to create agent window",
			params: launchParameters{
				sessionName:      "grove-test-job",
				container:        "my-agent",
				hostWorktreePath: "/path/to/repo/.grove-worktrees/test",
				containerWorkDir: "/workspace/test",
				agentCommand:     "claude 'test'",
			},
			setupMock: func(m *exec.MockCommandExecutor) {
				m.ExecuteFunc = func(name string, arg ...string) error {
					if name == "tmux" && len(arg) > 0 && arg[0] == "new-window" {
						return errors.New("failed to create window")
					}
					return nil
				}
			},
			expectedCmds: []string{
				"tmux new-session -d -s grove-test-job -c /path/to/repo/.grove-worktrees/test",
				"tmux new-window -t grove-test-job -n agent docker exec -it -w /workspace/test my-agent sh",
				"tmux kill-session -t grove-test-job", // Cleanup
			},
			expectError:   true,
			errorContains: "failed to create agent window",
		},
		{
			name: "Failed to send prompt",
			params: launchParameters{
				sessionName:      "grove-test-job",
				container:        "my-agent",
				hostWorktreePath: "/path/to/repo/.grove-worktrees/test",
				containerWorkDir: "/workspace/test",
				agentCommand:     "claude 'test'",
			},
			setupMock: func(m *exec.MockCommandExecutor) {
				m.ExecuteFunc = func(name string, arg ...string) error {
					if name == "tmux" && len(arg) > 0 && arg[0] == "send-keys" {
						return errors.New("failed to send prompt")
					}
					return nil
				}
			},
			expectedCmds: []string{
				"tmux new-session -d -s grove-test-job -c /path/to/repo/.grove-worktrees/test",
				"tmux new-window -t grove-test-job -n agent docker exec -it -w /workspace/test my-agent sh",
				"tmux send-keys -t grove-test-job:agent claude 'test' C-m",
				"tmux kill-session -t grove-test-job", // Cleanup
			},
			expectError:   true,
			errorContains: "failed to send prompt to tmux session",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExec := &exec.MockCommandExecutor{}
			if tt.setupMock != nil {
				tt.setupMock(mockExec)
			}
			
			err := launchTmuxSession(mockExec, tt.params)
			
			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
			
			// Verify the expected commands were executed
			assert.Equal(t, tt.expectedCmds, mockExec.Commands)
		})
	}
}

// Test helper to verify command structure
func TestCommandStructure(t *testing.T) {
	// Test that the agent command properly escapes complex prompts
	complexPrompt := `Implement the following:
1. User's authentication
2. It's important to handle edge cases
3. Don't forget error handling`
	
	job := &orchestration.Job{
		PromptBody: complexPrompt,
	}
	plan := &orchestration.Plan{}
	
	cmd, err := buildAgentCommand(job, plan, "/test", []string{})
	require.NoError(t, err)
	
	// The command should be properly escaped
	expected := "claude 'Implement the following:\n1. User'\\''s authentication\n2. It'\\''s important to handle edge cases\n3. Don'\\''t forget error handling'"
	assert.Equal(t, expected, cmd)
}