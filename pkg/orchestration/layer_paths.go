package orchestration

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/util/pathutil"
)

// Canonical file identity for the layer store (spec 19; oracle-plays job 25
// postmortem). The union diff, removal annotations, and supersede lists key
// on file PATHS, so two spellings of the same file must collapse to one key
// or a cross-worktree/cross-spelling turn re-uploads the whole fileset as
// "new" (the observed 02-add-a4b3c2.xml duplication). The canonical key is
// the path RELATIVE TO THE RESOLUTION ROOT (the job's worktree/sub-project
// dir — the same spelling healthy stores already record, e.g.
// "flow/pkg/model/model.go"), after absolute/symlink/case normalization.

// canonicalLayerRoot returns the canonical spelling of a layer resolution
// root: absolute, symlink-resolved, canonical-case (macOS). Used both for
// key normalization and for the manifest root pin.
func canonicalLayerRoot(root string) string {
	if c, err := pathutil.CanonicalPath(root); err == nil {
		return filepath.Clean(c)
	}
	if abs, err := filepath.Abs(root); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(root)
}

// canonicalLayerKey converts one resolver-spelled path into the store's
// canonical file identity relative to root:
//
//   - relative paths are already root-relative by construction (cx resolves
//     them against the context dir) — they are just cleaned, so healthy
//     stores keep their exact historical spelling (byte-stable layers);
//   - absolute paths under root (any spelling: symlinked, case-variant)
//     relativize against the canonical root;
//   - absolute paths under a FOREIGN checkout (the job-25 poisoning: the
//     resolver fell back to another worktree of the same superrepo) map to
//     the longest path suffix that exists under root — the same file's
//     identity in this worktree;
//   - anything else (a genuinely external file) keeps its canonical absolute
//     spelling, which is itself a stable identity.
func canonicalLayerKey(root, path string) string {
	if path == "" {
		return path
	}
	if !filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	rootCanon := canonicalLayerRoot(root)
	abs := canonicalLayerRoot(path)
	if rel, ok := relUnder(rootCanon, abs); ok {
		return rel
	}
	if suffix, ok := suffixUnderRoot(rootCanon, abs); ok {
		return suffix
	}
	return abs
}

// relUnder returns path relative to root when path is strictly inside root.
func relUnder(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// suffixUnderRoot finds the longest suffix of path's components that names an
// existing file or directory under root. This is what re-identifies a file
// recorded via a foreign checkout's absolute path (e.g.
// /Users/x/Code/eco/flow/pkg/a.go against an XDG worktree root that contains
// flow/pkg/a.go).
func suffixUnderRoot(root, path string) (string, bool) {
	trimmed := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	parts := strings.Split(trimmed, string(filepath.Separator))
	for i := 1; i < len(parts); i++ {
		suffix := filepath.Join(parts[i:]...)
		if _, err := os.Stat(filepath.Join(root, suffix)); err == nil {
			return suffix, true
		}
	}
	return "", false
}
