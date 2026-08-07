---
id: concept-map
title: "Build the LikeC4 concept map for {{ .PlanName }}"
status: pending
type: interactive_agent
skill: grove-concept-mapper
depends_on: []
---

{{- if .Vars.map }}
Build or update the LikeC4 architecture map `{{ .Vars.map }}`, stored in the notebook as a concept via `nb concept map`.
{{- else }}
Build or update a LikeC4 architecture map stored in the notebook as a concept via `nb concept map`. No map id was provided: derive a short kebab-case id from this plan's name (`{{ .PlanName }}`) and confirm it with the user before scaffolding.
{{- end }}
{{- if .Vars.repos }}

Scope the map to: {{ .Vars.repos }}.
{{- end }}

Follow the phases of your `grove-concept-mapper` skill rather than improvising:

1. **Survey** — read the existing concept library and the relevant code to learn what the map must describe. If the map concept already exists, read it first and treat this as a refresh, not a rewrite.
2. **Propose** — draft a map plan (elements, boundaries, relationships, views) and present it to the user.
3. **Gate** — get the user's approval of the plan before authoring anything.
4. **Author** — scaffold with `nb concept map new {{ if .Vars.map }}{{ .Vars.map }}{{ else }}<id>{{ end }}` if the map does not exist yet, then write the LikeC4 sources.
5. **Validate loop** — run `nb concept map validate {{ if .Vars.map }}{{ .Vars.map }}{{ else }}<id>{{ end }}` and fix issues until it passes cleanly.

Finish by serving the map with `nb concept map run {{ if .Vars.map }}{{ .Vars.map }}{{ else }}<id>{{ end }}` so the user can review it live, and tell them where it is being served.
