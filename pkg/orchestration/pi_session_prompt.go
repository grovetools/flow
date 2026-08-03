package orchestration

import (
	"fmt"
	"strings"
)

// pi_session_prompt.go holds the three pieces of prose a seeded session is born
// with: the shared framing, the per-job chat contract, and the kickoff briefing.
//
// They are separated by LIFETIME, not by topic. The framing is byte-identical
// for every pi-session job in the ecosystem and is written first, so the seed's
// leading bytes are stable across jobs — that costs nothing today (flat-plan Pi
// has no cache ladder to manage) and is what keeps the content-keyed caching
// option open later. The contract is job-specific. The briefing is not in the
// seed at all: it is the positional prompt, regenerated on every launch, so a
// resumed session gets current instructions rather than the ones it was born
// with.

// piSeedFraming is the shared oracle framing message. It is deliberately about
// EPISTEMICS rather than task instructions: what the session is holding, what
// that implies about its answers, and the two failure modes the bundle format
// makes easy (inventing what is absent, trusting line numbers that comment
// stripping moved).
func piSeedFraming() string {
	return strings.Join([]string{
		"You are running as a Grove Flow seeded design session.",
		"",
		"The message that follows this one is a curated context bundle: a frozen, deterministic sweep of source files, assembled by Grove from a rules file and delivered to you as `<layer>` blocks of `<file path=...>` entries. It was chosen deliberately. Treat it as your primary evidence.",
		"",
		"What that means for how you answer:",
		"",
		"1. The bundle is authoritative for what it contains. Anchor every claim to a file and a symbol you can actually see in it.",
		"2. The bundle is silent, not empty, about what it omits. If a question turns on something outside it, say so explicitly — 'the bundle does not contain X' is a correct and valuable answer. Do not reconstruct absent code from plausibility.",
		"3. Line numbers lie. Comments were stripped during assembly, so positions shifted. Anchor by symbol name (function, type, constant), never by line number.",
		"4. You still have tools and the real worktree. Use them to verify or to widen deliberately — but the bundle plus the rules file is the auditable record of what you were given, so prefer widening the record over accumulating unrecorded greps.",
		"",
		"You are a design partner, not an implementer: architecture, decomposition, trade-offs, and the specific reasons behind them.",
	}, "\n")
}

// piSeedContract is the per-job message binding the session to its chat file.
// It is prose rather than JSON because it addresses the model; the machine-
// readable form of the same facts is the launch descriptor (session.json),
// which the Phase 3 extension reads.
func piSeedContract(job *Job, plan *Plan) string {
	lines := []string{
		"## Your chat record",
		"",
		fmt.Sprintf("This session answers ONE Flow chat job: %s (plan %q, job id %s).", job.Title, plan.Name, job.ID),
		"",
		fmt.Sprintf("The canonical record of this conversation is the plan-level markdown file:\n\n    %s", job.FilePath),
		"",
		"That file — not this session transcript — is what humans read, what dependent jobs inline, and what survives this process. Every user turn arrives there first, and every response of yours belongs there.",
		"",
		"How turns move:",
		"",
		fmt.Sprintf("- A user turn is appended to the chat file by `flow plan say %s` (any agent, any machine, plain CLI). A wake sentinel at %s is bumped at the same time.", job.Filename, PiSessionWakePath(plan.Directory, job.ID)),
		fmt.Sprintf("- Your response goes back into the chat file with `flow plan respond %s`, which also moves the job to `pending_user` so a human knows it is their move.", job.Filename),
		"- The chat file is the truth and the wake sentinel is only a hint: if you are ever unsure whether you have seen everything, re-read the chat file.",
		"",
		"The dialogue ends when a human runs `flow plan complete` on the job; that gate is theirs, not yours.",
	}
	return strings.Join(lines, "\n")
}

// piSessionBriefing is the kickoff prompt written to the briefing file and
// pointed at by the launch command's positional argument. It is regenerated on
// every launch (including a resume), so it always describes the CURRENT state
// of the chat rather than the state at seed time.
func piSessionBriefing(job *Job, plan *Plan, desc PiSessionDescriptor) string {
	var b strings.Builder
	b.WriteString("<prompt>\n  <context>\n")
	fmt.Fprintf(&b, "    <role>Seeded Grove design session for chat job %s.</role>\n", job.Filename)
	fmt.Fprintf(&b, "    <chat_file>%s</chat_file>\n", job.FilePath)
	fmt.Fprintf(&b, "    <wake_file>%s</wake_file>\n", desc.WakeFile)
	fmt.Fprintf(&b, "    <session_descriptor>%s</session_descriptor>\n", PiSessionDescriptorPath(plan.Directory, job.ID))
	if desc.RulesFile != "" {
		fmt.Fprintf(&b, "    <rules_file>%s</rules_file>\n", desc.RulesFile)
	}
	fmt.Fprintf(&b, "    <context_dir>%s</context_dir>\n", desc.ContextDir)
	b.WriteString("  </context>\n\n  <task>\n")
	b.WriteString("Your context bundle is already loaded — it arrived with this session, so do NOT go re-read the codebase to find it.\n\n")
	b.WriteString("Do this now:\n")
	b.WriteString("1. Read the chat file named above, in full. It is the canonical record of this conversation and it may already contain turns.\n")
	b.WriteString("2. If it ends with a user turn you have not answered, answer it.\n")
	fmt.Fprintf(&b, "3. Write your answer back into the chat file with:\n\n       flow plan respond %s --file <your-response.md>\n\n", job.Filename)
	b.WriteString("   That command appends the response after the last marker and moves the job to pending_user.\n")
	b.WriteString("4. If there is no unanswered user turn, reply with a short readiness note (what you are holding, in one or two sentences) and then wait. New turns arrive in the chat file; the wake sentinel above changes when they do.\n")
	b.WriteString("  </task>\n</prompt>\n")
	return b.String()
}
