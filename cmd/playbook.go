package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/skills/pkg/skills"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// NewPlaybookCmd returns the top-level `flow playbook` command with
// show and list subcommands.
func NewPlaybookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "playbook",
		Short: "Inspect playbooks (versioned skill+prompt+recipe bundles)",
		Long: `Playbooks are versioned bundles of skills, prompts, recipes, and
references that together define a coherent methodology (e.g., gdv2).

Use these commands to inspect available playbooks and their contents.`,
	}
	cmd.AddCommand(newPlaybookShowCmd())
	cmd.AddCommand(newPlaybookListCmd())
	return cmd
}

func newPlaybookShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Print the overview for a playbook",
		Long: `Prints a human-readable overview of the named playbook: its manifest,
skills inventory, prompts, and recipes. With --json, emits a structured
JSON document.

If [name] is omitted, the current directory's .grove-plan.yml is read
and its active playbook (if any) is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				resolved, err := activePlaybookFromCWD()
				if err != nil {
					return err
				}
				name = resolved
			}
			if name == "" {
				return fmt.Errorf("no playbook name given and no active playbook in .grove-plan.yml")
			}
			pb, err := skills.LoadPlaybook(name)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(pb)
			}
			printPlaybook(cmd.OutOrStdout(), pb)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of human-readable text")
	return cmd
}

func newPlaybookListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all discoverable playbooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			playbooks := discoverPlaybooks()
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(playbooks)
			}
			if len(playbooks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No playbooks found.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVERSION\tDESCRIPTION")
			for _, pb := range playbooks {
				fmt.Fprintf(w, "%s\t%s\t%s\n",
					pb.Manifest.Name,
					pb.Manifest.Version,
					truncate(pb.Manifest.Description, 70),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of human-readable text")
	return cmd
}

// activePlaybookFromCWD walks upward from the current working directory
// looking for a .grove-plan.yml file and returns its `playbook:` field, if
// present. Returns the empty string and a nil error when no manifest is
// found.
func activePlaybookFromCWD() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ".grove-plan.yml")
		if content, err := os.ReadFile(path); err == nil {
			var cfg orchestration.PlanConfig
			if err := yaml.Unmarshal(content, &cfg); err == nil && cfg.Playbook != "" {
				return cfg.Playbook, nil
			}
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// discoverPlaybooks walks the global user playbooks dir and any
// search paths registered by the skills package.
func discoverPlaybooks() []*skills.Playbook {
	seen := make(map[string]*skills.Playbook)
	var roots []string

	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".config", "grove", "playbooks"))
	}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if _, err := os.Stat(filepath.Join(dir, "playbook.toml")); err != nil {
				continue
			}
			pb, err := skills.LoadPlaybookFromDir(dir)
			if err != nil {
				continue
			}
			seen[pb.Manifest.Name] = pb
		}
	}

	out := make([]*skills.Playbook, 0, len(seen))
	for _, pb := range seen {
		out = append(out, pb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out
}

// printPlaybook renders a human-readable overview of a playbook to the
// given writer.
func printPlaybook(w io.Writer, pb *skills.Playbook) {
	fmt.Fprintf(w, "PLAYBOOK: %s (v%s)\n", pb.Manifest.Name, pb.Manifest.Version)
	fmt.Fprintf(w, "PATH:     %s\n", pb.Path)
	if desc := strings.TrimSpace(pb.Manifest.Description); desc != "" {
		fmt.Fprintf(w, "DESC:     %s\n", desc)
	}
	if pb.Manifest.DefaultRecipe != "" {
		fmt.Fprintf(w, "DEFAULT:  %s\n", pb.Manifest.DefaultRecipe)
	}

	if len(pb.Skills) > 0 {
		fmt.Fprintln(w, "\nSKILLS:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range pb.Skills {
			fmt.Fprintf(tw, "  %s\t%s\n", s.Name, truncate(s.Description, 70))
		}
		tw.Flush()
	}

	if len(pb.Prompts) > 0 {
		fmt.Fprintln(w, "\nPROMPTS:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, p := range pb.Prompts {
			fmt.Fprintf(tw, "  %s\t%s\n", p.File, truncate(p.Purpose, 70))
		}
		tw.Flush()
	}

	if len(pb.Recipes) > 0 {
		fmt.Fprintln(w, "\nRECIPES:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, r := range pb.Recipes {
			fmt.Fprintf(tw, "  %s\t%s\n", r.File, truncate(r.Description, 70))
		}
		tw.Flush()
	}
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
