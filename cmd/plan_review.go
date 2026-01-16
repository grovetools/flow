package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var planReviewCmd = &cobra.Command{
	Use:   "review [directory]",
	Short: "Mark a plan as ready for review and execute completion hooks (use: flow review)",
	Long: `Marks a plan as ready for review, executes on-review hooks, and prepares it for final cleanup.
This is the intermediary step before using 'flow plan finish'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanReview,
}

// NewReviewCmd creates the top-level `review` command.
func NewReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review [directory]",
		Short: "Mark a plan as ready for review and execute completion hooks",
		Long: `Marks a plan as ready for review, executes on-review hooks, and prepares it for final cleanup.
This is the intermediary step before using 'flow finish'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanReview,
	}
}

// runPlanReview implements the review command.
func runPlanReview(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	planPath, err := resolvePlanPathWithActiveJob(dir)
	if err != nil {
		return err
	}

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	if plan.Config != nil && (plan.Config.Status == "review" || plan.Config.Status == "finished") {
		fmt.Printf("* Plan '%s' is already marked as '%s'. No action taken.\n", plan.Name, plan.Config.Status)
		fmt.Println("You can now proceed with final cleanup using 'flow plan finish'.")
		return nil
	}

	// Find the first job with a note_ref
	var noteRef string
	for _, job := range plan.Jobs {
		if job.NoteRef != "" {
			noteRef = job.NoteRef
			break
		}
	}

	// Execute on_review hook if it exists
	if plan.Config != nil && plan.Config.Hooks != nil {
		if hookCmdStr, ok := plan.Config.Hooks["on_review"]; ok && hookCmdStr != "" {
			fmt.Println("▶️  Executing on_review hook...")

			// Prepare template data
			templateData := struct {
				PlanName string
				NoteRef  string
			}{
				PlanName: plan.Name,
				NoteRef:  noteRef,
			}

			// Render the hook command
			tmpl, err := template.New("hook").Parse(hookCmdStr)
			if err != nil {
				return fmt.Errorf("failed to parse on_review hook template: %w", err)
			}
			var renderedCmd bytes.Buffer
			if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
				return fmt.Errorf("failed to render on_review hook command: %w", err)
			}

			// Execute the command
			hookCmd := exec.Command("sh", "-c", renderedCmd.String())
			hookCmd.Stdout = os.Stdout
			hookCmd.Stderr = os.Stderr
			if err := hookCmd.Run(); err != nil {
				return fmt.Errorf("on_review hook execution failed: %w", err)
			}
			fmt.Println("* on_review hook executed successfully.")
		}
	}

	// Update plan status to 'review'
	if plan.Config == nil {
		plan.Config = &orchestration.PlanConfig{}
	}
	plan.Config.Status = "review"

	configPath := filepath.Join(planPath, ".grove-plan.yml")

	// Read existing config to preserve other fields
	var existingConfig orchestration.PlanConfig
	if data, err := os.ReadFile(configPath); err == nil {
		yaml.Unmarshal(data, &existingConfig)
	}
	existingConfig.Status = "review"
	if plan.Config.Hooks != nil {
		existingConfig.Hooks = plan.Config.Hooks
	}

	yamlData, err := yaml.Marshal(existingConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal plan config: %w", err)
	}
	if err := os.WriteFile(configPath, yamlData, 0644); err != nil {
		return fmt.Errorf("failed to write updated plan config: %w", err)
	}

	fmt.Printf("* Plan '%s' marked for review.\n", plan.Name)
	fmt.Println("  You can now verify the results and then run 'flow plan finish' to clean up the worktree and branches.")

	return nil
}
