package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions, output io.Writer) (string, error)
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
func (c *CommandLLMClient) Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions, output io.Writer) (string, error) {
	args := []string{}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.SchemaPath != "" {
		args = append(args, "--schema", opts.SchemaPath)
	}

	// Track LLM request start
	requestStart := time.Now()
	// Get request ID from context or environment
	requestID, _ := ctx.Value("request_id").(string)
	if requestID == "" {
		requestID = os.Getenv("GROVE_REQUEST_ID")
	}
	c.log.WithFields(logrus.Fields{
		"request_id":    requestID,
		"job_id":        job.ID,
		"model":         opts.Model,
		"context_files": len(opts.ContextFiles),
		"prompt_source_files": len(opts.PromptSourceFiles),
		"prompt_length": len(prompt),
		"schema_path":   opts.SchemaPath,
	}).Info("Starting LLM request")
	
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
					c.log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Warn("Could not write prompt log file")
				} else {
					c.log.WithFields(logrus.Fields{
						"job_id": job.ID,
						"file":   logFilePath,
					}).Debug("Prompt saved to file")
				}
			}
		}
	
	// Log full command being executed
	c.log.WithField("args", strings.Join(args, " ")).Debug("Building grove llm command")

	// Grove llm request expects prompt as stdin when no positional args are given
	// We need to add "-" as the prompt argument to tell it to read from stdin
	cmd, err := c.cmdBuilder.Build(ctx, "grove", append([]string{"llm", "request"}, append(args, "-")...)...)
	if err != nil {
		return "", fmt.Errorf("building llm request command: %w", err)
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

	// Propagate request ID to child process via environment
	if requestID != "" {
		execCmd.Env = append(os.Environ(), "GROVE_REQUEST_ID="+requestID)
	}

	// Pipe full prompt (with all file contents) to stdin
	execCmd.Stdin = strings.NewReader(fullPrompt.String())
	c.log.Debug("Starting LLM command execution")
	
	// Log start time
	startTime := time.Now()

	// Create a log file for the command output
	logDir := ResolveLogDirectory(plan, job)
	logFileName := fmt.Sprintf("%s-%s-llm.log", job.ID, requestStart.Format("150405"))
	logFilePath := filepath.Join(logDir, logFileName)
	logFile, err := os.Create(logFilePath)
	if err != nil {
		c.log.WithError(err).Warn("Could not create LLM log file")
	} else {
		defer logFile.Close()
		writer := grovelogging.GetWriter(ctx)
		fmt.Fprintf(writer, "LLM output is being logged to: %s\n", logFilePath)
	}

	// Capture output and stream to the provided writer
	var stdout, stderr bytes.Buffer
	if logFile != nil {
		// Tee output to buffers, log file, and the live output writer
		execCmd.Stdout = io.MultiWriter(&stdout, logFile, output)
		execCmd.Stderr = io.MultiWriter(&stderr, logFile, output)
	} else {
		// Fallback if log file creation failed
		execCmd.Stdout = io.MultiWriter(&stdout, output)
		execCmd.Stderr = io.MultiWriter(&stderr, output)
	}

	if err := execCmd.Run(); err != nil {
		duration := time.Since(startTime)
		c.log.WithFields(logrus.Fields{
			"request_id":    requestID,
			"duration_ms":   duration.Milliseconds(),
			"error":         err.Error(),
			"stderr":        stderr.String(),
			"model":         opts.Model,
		}).Error("LLM request failed")
		return "", fmt.Errorf("llm command failed: %s: %w", stderr.String(), err)
	}

	duration := time.Since(startTime)
	responseLength := stdout.Len()
	
	// Calculate approximate token count (rough estimate: 1 token ≈ 4 chars)
	approxInputTokens := fullPrompt.Len() / 4
	approxOutputTokens := responseLength / 4
	
	c.log.WithFields(logrus.Fields{
		"request_id":      requestID,
		"duration_ms":     duration.Milliseconds(),
		"response_length": responseLength,
		"input_tokens_est": approxInputTokens,
		"output_tokens_est": approxOutputTokens,
		"model":          opts.Model,
	}).Info("LLM request completed")
	
	return stdout.String(), nil
}