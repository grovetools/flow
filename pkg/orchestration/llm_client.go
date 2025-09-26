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
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/sirupsen/logrus"
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
	Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions) (string, error)
}

// CommandLLMClient implements LLMClient using the llm command-line tool.
type CommandLLMClient struct {
	cmdBuilder *command.SafeBuilder
	log        *logrus.Entry
}

// NewCommandLLMClient creates a new LLM client that executes the llm command.
func NewCommandLLMClient() *CommandLLMClient {
	log := grovelogging.NewLogger("grove-flow")
	// Check if 'llm' command exists in PATH
	if _, err := exec.LookPath("llm"); err != nil {
		log.WithError(err).Warn("llm command not found in PATH")
	}
	return &CommandLLMClient{
		cmdBuilder: command.NewSafeBuilder(),
		log:        log,
	}
}

// Complete sends a prompt to the LLM and returns the response.
func (c *CommandLLMClient) Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions) (string, error) {
	args := []string{}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.SchemaPath != "" {
		args = append(args, "--schema", opts.SchemaPath)
	}
	
	// Log debug info
	c.log.WithFields(logrus.Fields{
		"model":         opts.Model,
		"context_files": len(opts.ContextFiles),
		"prompt_length": len(prompt),
	}).Debug("LLM call details")
	
	// Build full prompt with all file contents
	// Note: This is for non-Gemini models that don't support file attachments
	var fullPrompt strings.Builder
	
	// First add prompt source files if any
	if len(opts.PromptSourceFiles) > 0 {
		c.log.WithField("count", len(opts.PromptSourceFiles)).Debug("Adding prompt source files")
		
		for i, sourceFile := range opts.PromptSourceFiles {
			c.log.WithField("file", sourceFile).Debug("Adding prompt source")
			
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
		c.log.WithField("count", len(opts.ContextFiles)).Debug("Adding context files")
		
		for i, contextFile := range opts.ContextFiles {
			c.log.WithField("file", contextFile).Debug("Adding context from file")
			
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
	
	c.log.WithField("full_prompt_length", fullPrompt.Len()).Debug("Full prompt assembled")
		
		// Save prompt to debug log file
		if job != nil && plan != nil {
			logDir := ResolveLogDirectory(plan, job)
			promptLogDir := filepath.Join(logDir, "prompts")
			if err := os.MkdirAll(promptLogDir, 0755); err != nil {
				c.log.WithError(err).Warn("Could not create prompt log directory")
			} else {
				// Use a timestamp to make each log unique for a given job
				timestamp := time.Now().Format("20060102150405")
				logFileName := fmt.Sprintf("%s-%s-prompt.txt", job.ID, timestamp)
				logFilePath := filepath.Join(promptLogDir, logFileName)
				
				// Write the full prompt to the file
				if err := os.WriteFile(logFilePath, []byte(fullPrompt.String()), 0644); err != nil {
					c.log.WithError(err).Warn("Could not write prompt log file")
				} else {
					c.log.WithFields(logrus.Fields{
						"job_id": job.ID,
						"file":   logFilePath,
					}).Debug("Prompt saved to file")
				}
			}
		}
	
	// Log full command being executed
	c.log.WithField("args", strings.Join(args, " ")).Debug("Building llm command")
	
	cmd, err := c.cmdBuilder.Build(ctx, "llm", args...)
	if err != nil {
		return "", fmt.Errorf("building llm command: %w", err)
	}

	execCmd := cmd.Exec()
	
	// Log the actual command that will be executed
	c.log.WithFields(logrus.Fields{
		"path": execCmd.Path,
		"args": strings.Join(execCmd.Args[1:], " "),
	}).Debug("Actual exec command")
	
	// Set working directory if specified
	if opts.WorkingDir != "" {
		execCmd.Dir = opts.WorkingDir
		c.log.WithField("workdir", opts.WorkingDir).Debug("Working directory set")
	}

	// Pipe full prompt (with all file contents) to stdin
	execCmd.Stdin = strings.NewReader(fullPrompt.String())
	c.log.Debug("Starting LLM command execution")
	
	// Log start time
	startTime := time.Now()

	// Capture output
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	if err := execCmd.Run(); err != nil {
		duration := time.Since(startTime)
		c.log.WithFields(logrus.Fields{
			"duration": duration,
			"error":    err,
			"stderr":   stderr.String(),
		}).Debug("LLM command failed")
		return "", fmt.Errorf("llm command failed: %s: %w", stderr.String(), err)
	}

	duration := time.Since(startTime)
	c.log.WithFields(logrus.Fields{
		"duration":        duration,
		"response_length": stdout.Len(),
	}).Debug("LLM command succeeded")
	
	return stdout.String(), nil
}