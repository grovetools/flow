package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	
	// If we have context files, concatenate them into a single temp file
	var tempContextFile string
	if len(opts.ContextFiles) > 0 {
		// Create a temporary file for combined context
		// Use .txt extension to ensure it's recognized as text/plain
		tempFile, err := os.CreateTemp("", "grove-context-*.txt")
		if err != nil {
			return "", fmt.Errorf("creating temp context file: %w", err)
		}
		tempContextFile = tempFile.Name()
		defer os.Remove(tempContextFile) // Clean up when done
		
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Creating combined context file: %s\n", tempContextFile)
		}
		
		// Concatenate all context files
		for i, contextFile := range opts.ContextFiles {
			if os.Getenv("GROVE_DEBUG") != "" {
				fmt.Printf("[LLM DEBUG] Adding context from: %s\n", contextFile)
			}
			
			// Write header
			if i > 0 {
				tempFile.WriteString("\n\n")
			}
			tempFile.WriteString(fmt.Sprintf("=== Context from %s ===\n", contextFile))
			
			// Stream the context file to avoid loading into memory
			srcFile, err := os.Open(contextFile)
			if err != nil {
				tempFile.Close()
				return "", fmt.Errorf("opening context file %s: %w", contextFile, err)
			}
			
			// Copy file content
			_, err = io.Copy(tempFile, srcFile)
			srcFile.Close()
			if err != nil {
				tempFile.Close()
				return "", fmt.Errorf("copying context file %s: %w", contextFile, err)
			}
		}
		
		tempFile.Close()
		
		// Add the combined context file as an attachment with explicit text/plain mimetype
		if os.Getenv("GROVE_DEBUG") != "" {
			fmt.Printf("[LLM DEBUG] Adding combined context as attachment: %s\n", tempContextFile)
		}
		args = append(args, "--at", tempContextFile, "text/plain")
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

	// Pipe prompt to stdin
	execCmd.Stdin = strings.NewReader(prompt)
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