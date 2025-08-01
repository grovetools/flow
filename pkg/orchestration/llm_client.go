package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/command"
)

// LLMOptions defines configuration for an LLM completion request.
type LLMOptions struct {
	Model        string
	SchemaPath   string   // Path to JSON schema file for structured output
	WorkingDir   string   // Working directory for the LLM command
	ContextFiles []string // Paths to context files to pass directly to llm command
}

// LLMClient defines the interface for LLM interactions.
type LLMClient interface {
	Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error)
}

// CommandLLMClient implements LLMClient using the llm command-line tool.
type CommandLLMClient struct {
	cmdBuilder *command.SafeBuilder
}

// NewCommandLLMClient creates a new LLM client that executes the llm command.
func NewCommandLLMClient() *CommandLLMClient {
	// Check if 'llm' command exists in PATH
	if _, err := exec.LookPath("llm"); err != nil {
		// Log a warning or handle this appropriately. For now, nil is fine.
		// The error will be caught during execution.
	}
	return &CommandLLMClient{
		cmdBuilder: command.NewSafeBuilder(),
	}
}

// Complete sends a prompt to the LLM and returns the response.
func (c *CommandLLMClient) Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error) {
	args := []string{}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.SchemaPath != "" {
		args = append(args, "--schema", opts.SchemaPath)
	}
	
	// Log debug info
	fmt.Printf("[LLM DEBUG] Model: %s\n", opts.Model)
	fmt.Printf("[LLM DEBUG] Context files: %d\n", len(opts.ContextFiles))
	fmt.Printf("[LLM DEBUG] Prompt length: %d bytes\n", len(prompt))
	
	// Add context files as arguments to llm command
	for _, contextFile := range opts.ContextFiles {
		fmt.Printf("[LLM DEBUG] Adding context file: %s\n", contextFile)
		args = append(args, contextFile)
	}

	// Log full command being executed
	fmt.Printf("[LLM DEBUG] Building command: llm %s\n", strings.Join(args, " "))
	
	cmd, err := c.cmdBuilder.Build(ctx, "llm", args...)
	if err != nil {
		return "", fmt.Errorf("building llm command: %w", err)
	}

	execCmd := cmd.Exec()
	
	// Set working directory if specified
	if opts.WorkingDir != "" {
		execCmd.Dir = opts.WorkingDir
		fmt.Printf("[LLM DEBUG] Working directory: %s\n", opts.WorkingDir)
	}

	// Pipe prompt to stdin
	execCmd.Stdin = strings.NewReader(prompt)
	fmt.Printf("[LLM DEBUG] Starting LLM command execution...\n")
	
	// Log start time
	startTime := time.Now()

	// Capture output
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	if err := execCmd.Run(); err != nil {
		duration := time.Since(startTime)
		fmt.Printf("[LLM DEBUG] Command failed after %v\n", duration)
		fmt.Printf("[LLM DEBUG] Error: %v\n", err)
		fmt.Printf("[LLM DEBUG] Stderr: %s\n", stderr.String())
		return "", fmt.Errorf("llm command failed: %s: %w", stderr.String(), err)
	}

	duration := time.Since(startTime)
	fmt.Printf("[LLM DEBUG] Command succeeded after %v\n", duration)
	fmt.Printf("[LLM DEBUG] Response length: %d bytes\n", stdout.Len())
	
	return stdout.String(), nil
}