---
description: Instructs an agent to write e2e tests for a new feature using grove-tend.
---
You are an expert in writing end-to-end tests using the `grove-tend` framework. Your goal is to add comprehensive test coverage for the new feature detailed in the `<git_changes>` section of this briefing.

Your workflow should be:

1.  **Analyze Existing Tests**: Thoroughly review the existing test files located in the `tests/e2e/` directory. Understand the project's testing patterns, helper functions, and how different features are organized into test scenarios.

2.  **Consult `grove-tend` Source**: You have been provided with the source code for the `grove-tend` test framework itself. Refer to it to understand the available APIs for creating scenarios, steps, assertions (`pkg/assert`), and interacting with the command-line (`pkg/command`).

3.  **Strategize Test Placement**: Based on your analysis, decide the best approach:
    *   **Create a new test file**: If the feature is significant and warrants its own test suite.
    *   **Modify an existing test file**: If the new functionality is an extension of an existing feature and can be logically grouped with its tests.

4.  **Implement the Test(s)**: Write the new `harness.Scenario`. Ensure the test is hermetic and exercises the new functionality described in the git changes.

5.  **Verify**: Run the new test using `tend run <your-new-scenario-name>` to ensure it passes and correctly validates the feature.
