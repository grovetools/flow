package orchestration

// AgentJobTemplate is the template for agent jobs.
const AgentJobTemplate = `---
id: {{ .ID }}
title: "{{ .Title }}"
status: pending
type: {{ .Type }}
{{- if .DependsOn }}
depends_on:{{ range .DependsOn }}
  - {{ . }}{{ end }}{{ end }}{{ if .Include }}
include:{{ range .Include }}
  - {{ . }}{{ end }}{{ end }}{{ if .Repository }}
repository: {{ .Repository }}{{ end }}{{ if .Branch }}
branch: {{ .Branch }}{{ end }}{{ if .Worktree }}
worktree: {{ .Worktree }}{{ end }}{{ if .NoteRef }}
note_ref: {{ .NoteRef }}{{ end }}{{ if .SourceFile }}
source_file: {{ .SourceFile }}{{ end }}{{ if .Inline }}
inline:{{ range .Inline }}
  - {{ . }}{{ end }}{{ end }}{{ if .PrependDependencies }}
prepend_dependencies: true{{ end }}
---

{{ .Prompt }}
`
