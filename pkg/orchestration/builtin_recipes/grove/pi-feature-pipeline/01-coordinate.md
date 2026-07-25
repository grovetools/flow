---
id: pi-feature-pipeline
title: "Coordinate {{ .PlanName }} with Pi"
status: pending
type: interactive_agent
provider: pi
skill: grove-feature-pipeline
depends_on: []
---

Run the `grove-feature-pipeline` coordinator for this plan.

{{- if .Vars.flavor }}
Use pipeline flavor `{{ .Vars.flavor }}`.
{{- else }}
Use pipeline flavor `agent-verified-feature` unless task triage proves that `quick-fix` is appropriate.
{{- end }}

The selected YAML pipeline asset is executable input to `flow_pipeline`, not advisory context. Initialize or resume its persisted state, use `flow_subjob` for each child execution, and record each spawn/join/gate transition through `flow_pipeline`.
