package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Printf("[LLM DEBUG] Model: %s\n", opts.Model)
		fmt.Printf("[LLM DEBUG] Context files: %d\n", len(opts.ContextFiles))
		fmt.Printf("[LLM DEBUG] Prompt length: %d bytes\n", len(prompt))
	}
	
	// If we have context files, prepend them to the prompt
	var fullPrompt string
	if len(opts.ContextFiles) > 0 {
		var contextContent strings.Builder
		
		// Add all context files to the beginning of the prompt
		for i, contextFile := range opts.ContextFiles {
			if os.Getenv("GROVE_DEBUG") != "" {
				fmt.Printf("[LLM DEBUG] Adding context from: %s\n", contextFile)
			}
			
			// Write header
			if i > 0 {
				contextContent.WriteString("\n\n")
			}
			contextContent.WriteString(fmt.Sprintf("=== Context from %s ===\n", contextFile))
			
			// Read context file
			content, err := os.ReadFile(contextFile)
			if err != nil {
				return "", fmt.Errorf("reading context file %s: %w", contextFile, err)
			}
			contextContent.Write(content)
		}
		
		// Prepend context to the original prompt
		fullPrompt = contextContent.String() + "\n\n=== User Request ===\n\n" + prompt
		
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Full prompt length with context: %d bytes\n", len(fullPrompt))
		}
	} else {
		fullPrompt = prompt
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

	// Pipe full prompt (with context) to stdin
	execCmd.Stdin = strings.NewReader(fullPrompt)
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