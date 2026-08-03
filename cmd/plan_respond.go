package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// PlanRespondCmd is the testable core of `flow plan respond`: the response-side
// counterpart of `flow plan say`. It appends an agent-authored response turn to
// a chat job body and moves the job to pending_user.
//
// It exists because agent-authored chats (responder: agent, responder:
// pi-session) have no in-process writer for their responses. The oracle path
// writes its response inside executeChatJob while it already holds the file and
// the lock; an out-of-process responder — notably the Phase 3 Pi extension —
// would otherwise splice bytes into the .md itself and own marker grammar,
// locking, and the status transition by hand.
type PlanRespondCmd struct {
	Slug    string // optional plan slug (first positional when two are given)
	JobFile string // chat job filename (or slug/path when a separator is present)
	File    string // --file: path to the response text; stdin when empty
	Force   bool   // --force: append even without a pending user turn (recovery)
	Text    string // pre-supplied response text; bypasses --file/stdin (used by tests)

	Ctx context.Context
}

func (c *PlanRespondCmd) Run() error {
	return RunPlanRespond(c)
}

// RunPlanRespond resolves the target chat job and appends the response through
// the sanctioned persister writer.
func RunPlanRespond(c *PlanRespondCmd) error {
	text := c.Text
	if text == "" {
		var err error
		text, err = readRespondText(c.File)
		if err != nil {
			return err
		}
	}

	planDir, jobFile := resolveSayTarget(c.Ctx, c.Slug, c.JobFile)

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// The persister is the universal guard: chat-type check, responder check,
	// parse before and after, in-flight refusal, atomic write, shape tripwire.
	if err := orchestration.NewStatePersister().AppendChatResponseTurn(job, text, c.Force); err != nil {
		return err
	}

	// pending_user is the gate that hands the conversation back to the human.
	// It is stamped here rather than inside the writer so the status change goes
	// through UpdateJobStatus and fires the same notifications every other
	// terminal-ish transition does.
	if err := orchestration.NewStatePersister().UpdateJobStatus(job, orchestration.JobStatusPendingUser); err != nil {
		return fmt.Errorf("appended the response but could not move %s to pending_user: %w", jobFile, err)
	}

	fmt.Println(theme.DefaultTheme.Success.Render("*") + " Appended response to " + jobFile + " (status: pending_user)")
	fmt.Println("\nNext steps:")
	fmt.Printf("- Continue the dialogue: flow plan say %s\n", jobFile)
	fmt.Printf("- Finish it:            flow plan complete %s\n", jobFile)
	return nil
}

// readRespondText reads the response text from --file, or from stdin when no
// file is given. Refuses when neither is available so an empty invocation fails
// loud rather than appending nothing.
func readRespondText(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading response text file %s: %w", file, err)
		}
		return string(b), nil
	}
	return readSayText("")
}

// NewPlanRespondCmd builds the `flow plan respond` cobra command.
func NewPlanRespondCmd() *cobra.Command {
	var file string
	var force bool

	cmd := &cobra.Command{
		Use:   "respond [slug] <job-file>",
		Short: "Append an agent-authored response turn to a chat job body",
		Long: `Append a response turn to an agent-authored chat job body and move the job to
pending_user, validating the marker grammar before and after the write.

This is the response-side counterpart of 'flow plan say'. It applies only to
chats whose responses are authored by an agent rather than by a Flow-issued LLM
API call — responder: agent and responder: pi-session. Oracle chats produce
their responses through 'flow plan run' and are refused here.

The response text comes from --file, or from stdin when no --file is given. The
chat must end with a content-bearing user turn; pass --force only to recover a
response whose write was interrupted.

Examples:
  # A seeded pi session records its answer into the chat record
  flow plan respond 04-design.md --file answer.md

  # From stdin, targeting a plan by slug
  echo "Here is the design." | flow plan respond my-project 04-design.md`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &PlanRespondCmd{
				File:  file,
				Force: force,
				Ctx:   cmd.Context(),
			}
			if len(args) == 2 {
				c.Slug = args[0]
				c.JobFile = args[1]
			} else {
				c.JobFile = args[0]
			}
			return RunPlanRespond(c)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "File containing the response text (default: stdin)")
	cmd.Flags().BoolVar(&force, "force", false, "Append even when the chat does not end with a pending user turn (recovery)")
	return cmd
}
