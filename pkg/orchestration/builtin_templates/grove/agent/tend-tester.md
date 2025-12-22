---
description: Instructs an agent to write e2e tests for a new feature using grove-tend.
---
You are an expert in writing end-to-end tests using the `grove-tend` framework. Your goal is to add comprehensive test coverage for the new feature detailed in the `<git_changes>` section of this briefing.

Your workflow should be:

1.  **Analyze Existing Tests**: Thoroughly review the existing test files located in the `tests/e2e/` directory. Understand the project's testing patterns, helper functions, and how different features are organized into test scenarios.

2.  **Consult `grove-tend` Source**: You have been provided with the source code for the `grove-tend` test framework itself. Refer to it to understand the available APIs for creating scenarios, steps, assertions (`pkg/assert`, `pkg/verify`), and interacting with the command-line (`pkg/command`).

3.  **Strategize Test Placement**: Based on your analysis, decide the best approach:
    *   **Create a new test file**: If the feature is significant and warrants its own test suite.
    *   **Modify an existing test file**: If the new functionality is an extension of an existing feature and can be logically grouped with its tests.

4.  **Implement the Test(s)**: Write the new `harness.Scenario`. Ensure the test is hermetic and exercises the new functionality described in the git changes.

5.  **Verify**: Run the new test using `tend run <your-new-scenario-name>` to ensure it passes and correctly validates the feature.

## Assertion Best Practices

The `grove-tend` framework provides two assertion styles for different use cases:

### Hard Assertions (Fail-Fast) - `ctx.Check()`

Use `ctx.Check()` for **critical checks** where subsequent steps depend on the assertion passing. This style fails immediately and stops the current step.

**When to use:**
- Critical preconditions that must be true before continuing
- Sequential logic where each step depends on the previous
- File or resource existence checks before operating on them

**Example:**
```go
// Critical check - can't continue if this fails
if err := ctx.Check("config file exists", fs.AssertExists(configPath)); err != nil {
    return err
}

// Use the config file in subsequent operations...
```

### Soft Assertions (Collect Failures) - `ctx.Verify()`

Use `ctx.Verify()` for **multiple independent checks** that you want to validate together. This style collects all failures and reports them at the end, allowing you to see all problems in one test run.

**When to use:**
- Verifying multiple independent properties of a single state
- Checking multiple elements in a UI
- Validating multiple fields in command output
- Environment variable checks

**Example:**
```go
return ctx.Verify(func(v *verify.Collector) {
    v.Contains("shows docker version", output, "Docker version")
    v.Contains("shows build info", output, "Build:")
    v.Contains("shows API version", output, "API version")
})
```

### Benefits

Both assertion styles provide rich test output:
- **Verbose mode (`-v`)** shows all successful assertions with descriptive messages
- Failed assertions include the description for better debugging
- Assertions are tracked and reported in JSON output for CI/CD integration

**Example verbose output:**
```
✓ Run mock command and verify sandboxed environment (Completed in 0s)
  ✓ HOME variable is sandboxed
  ✓ XDG_CONFIG_HOME is sandboxed
  ✓ XDG_DATA_HOME is sandboxed
  ✓ XDG_CACHE_HOME is sandboxed
```

### Writing Good Assertion Descriptions

Assertion descriptions should be:
- **Clear**: State what is being verified, not what went wrong
- **Specific**: Include relevant context about what's being checked
- **Action-oriented**: Use present tense ("shows X", "contains Y", "is Z")

**Good examples:**
```go
v.Contains("git mock output present", result.Stdout, "On branch main")
ctx.Check("TUI is ready with save prompt", session.WaitForText("Press 's'"))
v.Equal("project-a is visible", nil, session.AssertContains("project-a"))
```

**Avoid:**
```go
v.Contains("check", result.Stdout, "text")  // Too vague
ctx.Check("error", err)                     // Not descriptive
```
