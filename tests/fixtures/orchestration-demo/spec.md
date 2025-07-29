# Specification: Add a Health Check Endpoint

## Goal
Add a `/health` endpoint to the existing Go web server.

## Requirements
1. The endpoint should be at the path `/health`.
2. It must respond with a `200 OK` status code.
3. The response body should be a JSON object containing `{"status": "ok"}`.
4. No external dependencies should be added.