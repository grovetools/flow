package orchestration

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// IsTransientError classifies whether an error should trigger a transient retry.
// Transient errors are those that may succeed on retry (e.g., timeouts, rate limits, connection failures).
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()

	// Timeout errors (context deadline exceeded, read timeout, etc.)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if strings.Contains(errMsg, "context deadline exceeded") ||
		strings.Contains(errMsg, "operation timed out") ||
		strings.Contains(errMsg, "i/o timeout") ||
		strings.Contains(errMsg, "read timeout") ||
		strings.Contains(errMsg, "write timeout") {
		return true
	}

	// HTTP 5xx errors (server errors)
	if strings.Contains(errMsg, "500") || // Internal server error
		strings.Contains(errMsg, "502") || // Bad gateway
		strings.Contains(errMsg, "503") || // Service unavailable
		strings.Contains(errMsg, "504") || // Gateway timeout
		strings.Contains(errMsg, "529") { // Too many requests (Anthropic specific)
		return true
	}

	// Connection errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		// net.Error includes timeouts and temporary errors
		return netErr.Temporary() || netErr.Timeout()
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Temporary network operations (connection reset, refused, etc.)
		return opErr.Temporary()
	}

	// Explicit connection messages
	if strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "connection timed out") ||
		strings.Contains(errMsg, "no such file or directory") && strings.Contains(errMsg, "dial") {
		return true
	}

	// ECONNREFUSED, ECONNRESET, ETIMEDOUT syscall errors
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		switch syscallErr {
		case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT:
			return true
		}
	}

	// EOF during reads (may indicate temporary network hiccup)
	if errors.Is(err, io.EOF) {
		return true
	}

	// Rate limit indicators
	if strings.Contains(errMsg, "rate limit") ||
		strings.Contains(errMsg, "too many requests") ||
		strings.Contains(errMsg, "overloaded") {
		return true
	}

	// Transient DNS resolution failures
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// Temporary DNS errors (not authoritative failures)
		return dnsErr.Temporary() || dnsErr.Timeout()
	}

	return false
}

// RetryableExecution wraps an execution function with automatic retry logic for transient failures.
// It sleeps with exponential backoff between attempts.
func RetryableExecution(maxRetries int, fn func() error) error {
	attempt := 0
	for {
		err := fn()
		if err == nil {
			return nil
		}

		// If this is not a transient error, fail immediately
		if !IsTransientError(err) {
			return err
		}

		// If we've exhausted retries, fail with the last error
		if attempt >= maxRetries-1 {
			return err
		}

		// Sleep with exponential backoff: 1s, 2s, 4s, etc.
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		time.Sleep(backoff)

		attempt++
	}
}
