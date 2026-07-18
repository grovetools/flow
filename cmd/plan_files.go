package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

// NewPlanFilesCmd builds the `flow plan files` cobra command: it prints the
// deduped accessed-files trace (.artifacts/<job>/accessed_files.jsonl) that a
// job's agent session left behind, for context transfer to successor jobs.
func NewPlanFilesCmd() *cobra.Command {
	var (
		jsonOut         bool
		workspaceRooted bool
		raw             bool
	)

	cmd := &cobra.Command{
		Use:   "files [slug] <job-file>",
		Short: "Print the files a job's agent session accessed",
		Long: `Print the deduped list of files a job's agent session read or modified,
from the job's .artifacts/<job>/accessed_files.jsonl trace.

By default prints absolute paths, one per line (agent-friendly). A job with no
trace prints nothing and exits 0.

Examples:
  # Accessed files of a job in the active plan
  flow plan files 05-my-job.md

  # Target a plan explicitly
  flow plan files 05-my-job.md --at my-plan

  # Workspace-rooted paths (<repo>/rel/path, worktree-unrooted)
  flow plan files 05-my-job.md --workspace-rooted

  # Full entries as JSON / the raw jsonl trace
  flow plan files 05-my-job.md --json
  flow plan files 05-my-job.md --raw`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var slug, jobArg string
			if len(args) == 2 {
				slug = args[0]
				jobArg = args[1]
			} else {
				jobArg = args[0]
			}
			return runPlanFiles(cmd, slug, jobArg, jsonOut, workspaceRooted, raw)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print full entries (path, action, count, last timestamp) as JSON")
	cmd.Flags().BoolVar(&workspaceRooted, "workspace-rooted", false, "Print workspace-rooted paths (<repo>/rel/path) instead of absolute")
	cmd.Flags().BoolVar(&raw, "raw", false, "Dump the accessed_files.jsonl trace as-is")
	return cmd
}

func runPlanFiles(cmd *cobra.Command, slug, jobArg string, jsonOut, workspaceRooted, raw bool) error {
	planDir, jobFile := resolveSayTarget(cmd.Context(), slug, jobArg)

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	job, err := findFilesJob(plan, jobFile)
	if err != nil {
		return err
	}

	tracePath := orchestration.AccessedFilesPath(plan.Directory, job)

	if raw {
		if tracePath == "" {
			return nil
		}
		data, err := os.ReadFile(tracePath)
		if err != nil {
			return fmt.Errorf("read trace: %w", err)
		}
		_, err = os.Stdout.Write(data)
		return err
	}

	files, err := orchestration.ReadAccessedFiles(tracePath, orchestration.AccessedFilesBase(plan, job))
	if err != nil {
		return fmt.Errorf("read trace: %w", err)
	}

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if files == nil {
			files = []orchestration.AccessedFile{}
		}
		return encoder.Encode(files)
	}

	if workspaceRooted {
		provider, err := orchestration.NewDisplayWorkspaceProvider(cmd.Context())
		if err != nil {
			return fmt.Errorf("resolve workspaces for --workspace-rooted: %w", err)
		}
		for _, f := range files {
			fmt.Println(orchestration.WorkspaceRootedPath(provider, f.Path))
		}
		return nil
	}

	for _, f := range files {
		fmt.Println(f.Path)
	}
	return nil
}

// findFilesJob resolves the job argument leniently: exact filename, filename
// with .md appended, then job ID.
func findFilesJob(plan *orchestration.Plan, jobFile string) (*orchestration.Job, error) {
	if job, ok := plan.GetJobByFilename(jobFile); ok {
		return job, nil
	}
	if job, ok := plan.GetJobByFilename(jobFile + ".md"); ok {
		return job, nil
	}
	if job, ok := plan.GetJobByID(jobFile); ok {
		return job, nil
	}
	return nil, fmt.Errorf("job not found: %s", jobFile)
}
