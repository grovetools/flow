package orchestration

import (
	"context"
	"io"

	grovelogging "github.com/mattsolo1/grove-core/logging"
)

type contextKey string

const jobOutputWriterKey contextKey = "job_output_writer"

// GetJobWriter retrieves the job-specific output writer from context.
// It falls back to the global logger output if no writer is found in the context.
// This ensures backward compatibility with functions that have not yet been updated.
func GetJobWriter(ctx context.Context) io.Writer {
	if writer, ok := ctx.Value(jobOutputWriterKey).(io.Writer); ok && writer != nil {
		return writer
	}
	// Fallback to the global logger for backward compatibility and non-job contexts.
	return grovelogging.GetGlobalOutput()
}

// WithJobWriter returns a new context with the job-specific output writer attached.
func WithJobWriter(ctx context.Context, writer io.Writer) context.Context {
	return context.WithValue(ctx, jobOutputWriterKey, writer)
}
