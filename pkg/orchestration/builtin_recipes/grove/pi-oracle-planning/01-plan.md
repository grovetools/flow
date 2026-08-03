---
id: pi-oracle-planning
title: "Plan {{ .PlanName }} with a seeded Pi oracle"
status: pending
type: interactive_agent
provider: pi
skill: grove-pi-oracle
coord_mode: autonomous
depends_on: []
---

Run the planner seat of `grove-pi-oracle` for this plan: read the skill's `references/planner.md` in full before acting.

{{- if .Vars.feature }}
Design target: {{ .Vars.feature }}
{{- else }}
Design target: the ticket or brief the operator gives you in the first turn. Ask for it before curating anything.
{{- end }}

You are the PLANNING half of a two-coordinator arc, and both halves are one lineage:

1. You curate a rules file, then create the oracle as a `type: chat`, `responder: pi-session` job in THIS plan with `--parent-job-id <your job id>`, so the seeded session is owned by you (vertical lineage). `flow_subjob create` cannot make that job — it only creates `interactive_agent` children — so use `flow plan add` directly and keep the `--parent-job-id` lineage.
{{- if .Vars.oracle_model }}
   Launch the oracle on `--model {{ .Vars.oracle_model }}`.
{{- else }}
   Pick a big-window model for the oracle: the seed IS its epistemics and the session never compacts.
{{- end }}
2. You drive the chat with `flow plan say` and read the responses back out of the same chat file. The chat `.md` is the record; the Pi session is only the engine.
3. Before any gate you spawn an adversarial verifier as a real `flow_subjob` child — a fresh agent that reads the code and hunts for what is WRONG — and join its report. **No verifier report means the design is not gateable**, however convincing it reads.
4. After the gate you materialize the phased plan the oracle emitted: plain `flow plan add` calls with `depends_on` DAGs, batch-created **pending and unlaunched** so the operator reviews the roster before anything runs.
5. You then call `flow_handoff` with `action: handoff`, `skill: grove-pi-oracle`, and a spec whose OPENING LINE declares the seat ("You are the EXECUTOR seat of grove-pi-oracle — read references/executor.md"), which creates the executor coordinator (horizontal lineage) and ends this session. The executor dispatches the impl children; you never do.

`coord_mode: autonomous` is set because the handoff in step 5 is a planned workflow step, not a context-exhaustion escape. It also lets you hand off early if your own window fills first — the chain is bounded by `handoff_max`. Set `coord_mode: manual` in this job's frontmatter instead if the operator wants to authorize each handoff with `/handoff`.
