Based on the specification, I'll create a plan to add the health endpoint.

## Overview
We'll implement this in two steps: first add the handler function, then register the route.

## Job Files Created

---
id: 20250719-120000-health-handler
title: "Implement Health Handler"
status: pending
type: agent
depends_on: []
worktree: feature-health-endpoint
output:
  type: commit
  message: "feat: add health endpoint handler"
---
Add a healthHandler function to main.go that returns the required JSON response.

---
id: 20250719-120100-register-route
title: "Register Health Route"
status: pending
type: agent
depends_on:
  - 20250719-120000-health-handler
worktree: feature-health-endpoint
output:
  type: commit
  message: "feat: register /health endpoint"
---
Register the /health route in the main function.