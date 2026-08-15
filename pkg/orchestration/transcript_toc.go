package orchestration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/agentlogs/pkg/toc"
	"github.com/grovetools/agentlogs/pkg/transcript"
	coresessions "github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
)

// Rendered table-of-contents artifacts written into a job's artifact dir at
// terminal/turn-complete seams. The styled variant carries ANSI escapes (the
// toc renderer pins a real color profile off-terminal, so it is deterministic
// without a TTY); the plain variant is the same layout with escapes stripped.
const (
	// TranscriptTocStyledName is the ANSI-styled outline artifact.
	TranscriptTocStyledName = "toc.ansi"
	// TranscriptTocPlainName is the plain-text outline artifact.
	TranscriptTocPlainName = "toc.txt"
)

// transcriptTocWidth is the fixed render width for the persisted outline.
// Consumers that need another width re-render from the transcript; the
// artifact is a stable, human-readable snapshot, not a reflowable source.
const transcriptTocWidth = 120

// NormalizeTranscriptFile reads a jsonl transcript and normalizes each line
// with the given provider's normalizer (agentstream.NormalizerForProvider —
// empty/unknown falls back to the Claude normalizer). Lines that fail to
// normalize are skipped; buffered entries (tool calls awaiting results) are
// flushed at EOF. The scanner tolerates multi-megabyte lines, which real tool
// results produce routinely.
//
// Exported so other flow surfaces (e.g. the status TUI) can share the exact
// parse the TOC artifacts were built from.
func NormalizeTranscriptFile(path, provider string) ([]transcript.UnifiedEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	normalizer := agentstream.NormalizerForProvider(provider)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var entries []transcript.UnifiedEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := normalizer.NormalizeLine(line)
		if err != nil || entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}
	if flusher, ok := normalizer.(agentstream.Flusher); ok {
		for _, entry := range flusher.Flush() {
			entries = append(entries, *entry)
		}
	}
	return entries, scanner.Err()
}

// ResolveTranscriptForOutline is resolveTocTranscript for callers outside the
// package: the status TUI's outline offer hands treemux the same transcript
// this package would build a TOC from, so the pinned outline and the toc.ansi
// artifact can never disagree about which file describes a job.
func ResolveTranscriptForOutline(job *Job, plan *Plan) (path string, metadata *coresessions.SessionMetadata, ok bool) {
	return resolveTocTranscript(job, plan)
}

// resolveTocTranscript finds the transcript the TOC will be built from,
// together with whatever session metadata describes it. Order mirrors the rest
// of completion: the verified registry binding / live artifact transcript
// first (resolveJobTranscript), then the archived copy ArchiveInteractiveSession
// leaves at .artifacts/<jobID>/transcript.jsonl. Returns ok=false when the job
// simply has no transcript — which is a skip, never an error.
func resolveTocTranscript(job *Job, plan *Plan) (path string, metadata *coresessions.SessionMetadata, ok bool) {
	if source, err := resolveJobTranscript(job, plan); err == nil && source != nil && source.TranscriptPath != "" {
		if info, statErr := os.Stat(source.TranscriptPath); statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return source.TranscriptPath, source.Metadata, true
		}
	}

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	archived := filepath.Join(artifactDir, "transcript.jsonl")
	if info, err := os.Stat(archived); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		var md coresessions.SessionMetadata
		if data, readErr := os.ReadFile(filepath.Join(artifactDir, "metadata.json")); readErr == nil && json.Unmarshal(data, &md) == nil {
			return archived, &md, true
		}
		return archived, nil, true
	}
	return "", nil, false
}

// transcriptTocMarkers binds the outline's $WT/$JA/$NB path markers to this
// job's actual directories. Every field is best-effort: an empty marker simply
// disables that substitution (PathMarkers.Match skips empty dirs).
func transcriptTocMarkers(job *Job, plan *Plan, metadata *coresessions.SessionMetadata) toc.PathMarkers {
	markers := toc.PathMarkers{
		Artifacts: filepath.Join(plan.Directory, ".artifacts", job.ID),
	}
	// The session's own working directory is the truthful $WT: it is where the
	// agent actually ran, whether that was a worktree container or the plan's
	// host checkout. Reconstructed metadata (artifact-transcript fallback) has
	// no working directory, and that is fine — the marker just stays off.
	if metadata != nil && metadata.WorkingDirectory != "" {
		markers.Worktree = metadata.WorkingDirectory
	}
	if _, notebookRoot, _ := workspace.GetProjectFromNotebookPath(plan.Directory); notebookRoot != "" {
		markers.Notebook = notebookRoot
	} else {
		markers.Notebook = plan.Directory
	}
	return markers
}

// WriteTranscriptToc renders the job's agent-transcript outline and persists
// it into the job's artifact dir as toc.ansi (ANSI-styled) and toc.txt
// (plain). A job with no resolvable transcript, or a transcript that yields no
// entries, writes nothing and returns nil. Writes are atomic-rename so a
// reader never observes a half-written outline — chat jobs rewrite these every
// turn.
func WriteTranscriptToc(job *Job, plan *Plan) error {
	if job == nil || plan == nil || job.ID == "" || plan.Directory == "" {
		return fmt.Errorf("transcript toc: nil or incomplete job/plan")
	}

	transcriptPath, metadata, ok := resolveTocTranscript(job, plan)
	if !ok {
		logrus.WithField("job_id", job.ID).Debug("transcript toc: no transcript for job; skipping")
		return nil
	}

	provider := job.Provider
	if provider == "" && metadata != nil {
		provider = metadata.Provider
	}

	entries, err := NormalizeTranscriptFile(transcriptPath, provider)
	if err != nil {
		return fmt.Errorf("transcript toc: parse %s: %w", transcriptPath, err)
	}
	if len(entries) == 0 {
		logrus.WithField("job_id", job.ID).Debug("transcript toc: transcript yielded no entries; skipping")
		return nil
	}

	items := toc.BuildAgentItems(entries, toc.BuildOptions{
		Provider: provider,
		Markers:  transcriptTocMarkers(job, plan, metadata),
	})
	if len(items) == 0 {
		logrus.WithField("job_id", job.ID).Debug("transcript toc: outline is empty; skipping")
		return nil
	}

	renderOpts := toc.DefaultRenderOptions(transcriptTocWidth, provider)
	styled := toc.RenderStyled(items, renderOpts)
	plain := toc.RenderPlain(items, renderOpts)

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := writeArtifactFileAtomic(artifactDir, TranscriptTocStyledName, []byte(styled)); err != nil {
		return fmt.Errorf("transcript toc: write %s: %w", TranscriptTocStyledName, err)
	}
	if err := writeArtifactFileAtomic(artifactDir, TranscriptTocPlainName, []byte(plain)); err != nil {
		return fmt.Errorf("transcript toc: write %s: %w", TranscriptTocPlainName, err)
	}
	return nil
}

// writeTranscriptTocQuietly attaches the TOC writer at a completion seam. A
// capture failure is warned and swallowed: there must be no path from a TOC
// failure to a job failure (same contract as writeMetricsRecordQuietly).
func writeTranscriptTocQuietly(job *Job, plan *Plan) {
	if err := WriteTranscriptToc(job, plan); err != nil {
		logrus.Warnf("failed to write transcript toc: %v", err)
	}
}

// writeArtifactFileAtomic writes name into dir via CreateTemp+Sync+Rename with
// 0600 perms, so concurrent readers only ever see a complete file.
func writeArtifactFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}
