package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/grovetools/core/pkg/plan"
	"github.com/spf13/cobra"
)

// targetKeyType is a private type for the unified-target context key so it
// cannot collide with keys from other packages stored on the same context.
type targetKeyType struct{}

// TargetContextKey is the context key under which the resolved `--at` target
// (*plan.ResolvedTarget) is stashed by the root PersistentPreRunE. Leaf
// commands read it via TargetFromContext.
var TargetContextKey = targetKeyType{}

// satelliteKeyType is a private type for the satellite-dispatch context key.
type satelliteKeyType struct{}

// SatelliteContextKey is the context key under which a `--at satellite:<name>`
// dispatch target's satellite NAME is stashed by the root PersistentPreRunE.
// It is set INSTEAD of TargetContextKey (satellite dispatch does not resolve a
// local worktree target); leaf commands read it via SatelliteFromContext.
var SatelliteContextKey = satelliteKeyType{}

// SatellitePrefix is the `--at` prefix that routes a plan run to a satellite.
const SatellitePrefix = "satellite:"

// SatelliteFromContext returns the satellite name stashed on ctx when `--at`
// used the satellite: prefix, or ("", false) otherwise.
func SatelliteFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	name, ok := ctx.Value(SatelliteContextKey).(string)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// TargetFromContext returns the resolved target stashed on ctx by the root
// PersistentPreRunE, or (nil, false) when `--at` was not provided.
func TargetFromContext(ctx context.Context) (*plan.ResolvedTarget, bool) {
	if ctx == nil {
		return nil, false
	}
	target, ok := ctx.Value(TargetContextKey).(*plan.ResolvedTarget)
	if !ok || target == nil {
		return nil, false
	}
	return target, true
}

// TargetFlagName is the long name of the unified target flag.
const TargetFlagName = "at"

// SetupTargetFlag registers the persistent `--at` flag on rootCmd and attaches
// a PersistentPreRunE that resolves it ONCE into a *plan.ResolvedTarget and
// stashes it on the leaf command's context.
//
// PersistentPreRunE is the correct site: `--at` is a flag that cobra does not
// parse until Execute-time, so the resolved target cannot be computed before
// Execute() (the cx "SetContext before Execute" pattern does not apply here).
// The cmd passed to PersistentPreRunE is the LEAF command, and cmd.SetContext
// on it carries through to the leaf's RunE.
func SetupTargetFlag(rootCmd *cobra.Command) {
	rootCmd.PersistentFlags().String(TargetFlagName, "", "Target plan or worktree to operate on (plan name, container path, or <container-id>/<name>)")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		ref, _ := cmd.Flags().GetString(TargetFlagName)
		explicit := ref != ""

		// Alias + deprecation-warn: when --at is empty, fall back to the
		// legacy --dir/--plan/--workspace flags (if present) and route their
		// value through the same resolver, emitting a one-line stderr notice.
		if ref == "" {
			ref = legacyTargetRef(cmd)
		}

		if ref == "" {
			return nil
		}

		// Satellite dispatch: `--at satellite:<name>` routes the run to a
		// satellite rather than a local worktree. Intercept BEFORE
		// plan.ResolveTarget (which stays worktree-only — F2). The local plan
		// dir is resolved by the leaf command from its positional args.
		if satName, isSat := strings.CutPrefix(ref, SatellitePrefix); isSat {
			satName = strings.TrimSpace(satName)
			if satName == "" {
				return fmt.Errorf("--at %s requires a satellite name (e.g. --at satellite:grove-satellite)", SatellitePrefix)
			}
			cmd.SetContext(context.WithValue(cmd.Context(), SatelliteContextKey, satName))
			return nil
		}

		target, err := plan.ResolveTarget(ref)
		if err != nil {
			// An explicit --at that fails to resolve is a hard error. A legacy
			// --dir/--plan/--workspace alias is best-effort convenience, NOT a
			// gate: commands like `plan review`/`finish` use --dir as a plain
			// working-directory context (the ecosystem root, which is not a
			// registered worktree), so a resolution miss must fall through to
			// the command's own flag handling rather than abort it. Aliasing
			// it to --at only when it genuinely resolves to a worktree keeps
			// the convenience without breaking the directory-context use.
			if explicit {
				return fmt.Errorf("resolve --at target %q: %w", ref, err)
			}
			return nil
		}
		cmd.SetContext(context.WithValue(cmd.Context(), TargetContextKey, target))
		return nil
	}
}

// legacyTargetRef inspects the deprecated --dir/--plan/--workspace flags on
// cmd and, if one is explicitly set, returns its value after emitting a
// one-line deprecation warning to stderr. Returns "" when none is set.
//
// These flags are command-local (registered per-subcommand), so the lookup is
// best-effort: a flag that the leaf command does not define is simply skipped.
func legacyTargetRef(cmd *cobra.Command) string {
	for _, name := range []string{"dir", "plan", "workspace"} {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		val := f.Value.String()
		if val == "" || val == "." {
			continue
		}
		fmt.Fprintf(os.Stderr, "Warning: --%s is deprecated; use --at %s instead\n", name, val)
		return val
	}
	return ""
}
