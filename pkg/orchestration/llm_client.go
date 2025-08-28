package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/command"
)

// LLMOptions defines configuration for an LLM completion request.
type LLMOptions struct {
	Model             string
	SchemaPath        string   // Path to JSON schema file for structured output
	WorkingDir        string   // Working directory for the LLM command
	ContextFiles      []string // Paths to context files (.grove/context, CLAUDE.md)
	PromptSourceFiles []string // Paths to prompt source files from job configuration
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
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Model: %s\n", opts.Model)
		fmt.Printf("[LLM DEBUG] Context files: %d\n", len(opts.ContextFiles))
		fmt.Printf("[LLM DEBUG] Prompt length: %d bytes\n", len(prompt))
	}
	
	// Build full prompt with all file contents
	// Note: This is for non-Gemini models that don't support file attachments
	var fullPrompt strings.Builder
	
	// First add prompt source files if any
	if len(opts.PromptSourceFiles) > 0 {
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Adding %d prompt source files\n", len(opts.PromptSourceFiles))
		}
		
		for i, sourceFile := range opts.PromptSourceFiles {
			if os.Getenv("GROVE_DEBUG") != "" {
				fmt.Printf("[LLM DEBUG] Adding prompt source: %s\n", sourceFile)
			}
			
			if i > 0 {
				fullPrompt.WriteString("\n\n")
			}
			fullPrompt.WriteString(fmt.Sprintf("=== Source: %s ===\n", filepath.Base(sourceFile)))
			
			content, err := os.ReadFile(sourceFile)
			if err != nil {
				return "", fmt.Errorf("reading prompt source file %s: %w", sourceFile, err)
			}
			fullPrompt.Write(content)
		}
		fullPrompt.WriteString("\n\n")
	}
	
	// Then add context files
	if len(opts.ContextFiles) > 0 {
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Adding %d context files\n", len(opts.ContextFiles))
		}
		
		for i, contextFile := range opts.ContextFiles {
			if os.Getenv("GROVE_DEBUG") != "" {
				fmt.Printf("[LLM DEBUG] Adding context from: %s\n", contextFile)
			}
			
			if i > 0 || opts.PromptSourceFiles != nil {
				fullPrompt.WriteString("\n\n")
			}
			fullPrompt.WriteString(fmt.Sprintf("=== Context from %s ===\n", filepath.Base(contextFile)))
			
			content, err := os.ReadFile(contextFile)
			if err != nil {
				return "", fmt.Errorf("reading context file %s: %w", contextFile, err)
			}
			fullPrompt.Write(content)
		}
		fullPrompt.WriteString("\n\n")
	}
	
	// Finally add the actual prompt
	if fullPrompt.Len() > 0 {
		fullPrompt.WriteString("=== User Request ===\n\n")
	}
	fullPrompt.WriteString(prompt)
	
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Full prompt length: %d bytes\n", fullPrompt.Len())
	}

	// Log full command being executed
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Building command: llm %s\n", strings.Join(args, " "))
	}
	
	cmd, err := c.cmdBuilder.Build(ctx, "llm", args...)
	if err != nil {
		return "", fmt.Errorf("building llm command: %w", err)
	}

	execCmd := cmd.Exec()
	
	// Log the actual command that will be executed
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Actual exec command: %s %s\n", execCmd.Path, strings.Join(execCmd.Args[1:], " "))
	}
	
	// Set working directory if specified
	if opts.WorkingDir != "" {
		execCmd.Dir = opts.WorkingDir
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Working directory: %s\n", opts.WorkingDir)
		}
	}

	// Pipe full prompt (with all file contents) to stdin
	execCmd.Stdin = strings.NewReader(fullPrompt.String())
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Starting LLM command execution...\n")
	}
	
	// Log start time
	startTime := time.Now()

	// Capture output
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	if err := execCmd.Run(); err != nil {
		duration := time.Since(startTime)
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Command failed after %v\n", duration)
			fmt.Printf("[LLM DEBUG] Error: %v\n", err)
			fmt.Printf("[LLM DEBUG] Stderr: %s\n", stderr.String())
		}
		return "", fmt.Errorf("llm command failed: %s: %w", stderr.String(), err)
	}

	duration := time.Since(startTime)
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Command succeeded after %v\n", duration)
		fmt.Printf("[LLM DEBUG] Response length: %d bytes\n", stdout.Len())
	}
	
	return stdout.String(), nil
}