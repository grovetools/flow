package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// pi_session_seed.go is the Go-native writer for a synthesized Pi session
// (session JSONL v3 — the "push half" of the session-seeding design). It turns
// a curated context bundle into a session file that `pi --session <path>` opens
// as if the conversation had already happened, so the agent wakes up holding
// the whole bundle without a single tool call.
//
// # Why Go-native rather than a Node shim
//
// The seeding design originally recommended prototyping through pi's own
// SessionManager (via the grove-pi package) to eliminate format risk. Three
// facts retire that recommendation:
//
//  1. The format is trivially permissive on the read side. pi's
//     loadEntriesFromFile → parseSessionEntryLine does `JSON.parse` and nothing
//     else: no schema validation, no required-field checks, unknown fields
//     preserved. There is no validator for a Go writer to disagree with.
//  2. Grove ALREADY owns a Go implementation of the read side.
//     agentlogs/pkg/transcript/normalizer_pi.go parses exactly these entries for
//     the transcript pipeline, and flow already depends on it. That gives the
//     round-trip fixture a real, independently-maintained parser to check
//     against (TestPiSessionSeedRoundTrip) rather than a self-consistent
//     encode/decode pair.
//  3. A shim would make the launch path depend on Node being installed and on
//     the grove-pi package being resolvable BEFORE pi itself starts — a new
//     failure class on the one path that must not have one, and one that the
//     probe's second-turn hang already suggests is not free.
//
// The residual risk is a future pi bumping CURRENT_SESSION_VERSION. That is
// handled explicitly rather than implicitly: piSessionFormatVersion below is the
// pinned contract, ProbePiSessionFormat records the runtime's own version
// stamp beside the seed, and the fixture test fails loudly if the entry shapes
// drift.

// piSessionFormatVersion is the session-file schema version this writer emits.
// It mirrors CURRENT_SESSION_VERSION in pi's
// packages/coding-agent/src/core/session-manager.ts, verified against the
// runtime pinned in agent/runtime-pins.json (@earendil-works/pi-coding-agent
// 0.80.10). pi migrates OLDER files forward (migrateV1ToV2 / migrateV2ToV3) and
// rewrites them in place, so emitting the current version is what keeps the
// seed byte-stable on disk.
const piSessionFormatVersion = 3

// Seed entry customTypes. These are the identity the Phase 3 extension keys on
// when it reconstructs its state from the session on reload, so they are part
// of the cross-phase contract and must not be renamed casually.
const (
	// PiSeedStampType marks the out-of-context `custom` entry carrying job
	// identity. `custom` entries are ignored by pi's buildSessionContext, so
	// this persists the binding without spending a single prompt token.
	PiSeedStampType = "grove_flow_pi_session"
	// PiSeedFramingType marks the in-context oracle framing message.
	PiSeedFramingType = "grove_flow_seed_framing"
	// PiSeedBundleType marks the in-context frozen-layer bundle.
	PiSeedBundleType = "grove_flow_seed_bundle"
	// PiSeedContractType marks the in-context chat-protocol message telling the
	// session which file it is speaking into and how turns arrive.
	PiSeedContractType = "grove_flow_seed_contract"
)

// PiSeedMessage is one in-context `custom_message` entry of the seed. Content is
// always a plain string: pi accepts `string | (TextContent|ImageContent)[]` and
// converts either to a user message, and a string keeps the bytes (and therefore
// any future content-keyed cache identity) exactly reproducible.
type PiSeedMessage struct {
	// CustomType identifies the injecting subsystem; one of the PiSeed*Type
	// constants above.
	CustomType string
	// Content is the message body delivered into the model's context.
	Content string
	// Display controls pi's TUI rendering: false hides the entry entirely.
	// The bundle is hidden (it is hundreds of KB of XML); the framing and
	// contract are shown so a human attaching to the pane can see the rules the
	// session is operating under.
	Display bool
}

// PiSessionSeed is one synthesized session file's worth of input.
type PiSessionSeed struct {
	// SessionID is the pi session id stamped in the header. Empty generates a
	// fresh uuidv7 (the shape pi's own createSessionId produces).
	SessionID string
	// CWD is written into the header. pi's SessionManager.open uses the HEADER's
	// cwd as the agent's working directory, so this must be the job's resolved
	// context directory, not the launcher's cwd.
	CWD string
	// Stamp is the payload of the out-of-context identity `custom` entry.
	Stamp map[string]any
	// Messages are the in-context entries, emitted in slice order. Order is
	// significant: shared content (framing) is written before job-specific
	// content (bundle, contract) so the byte prefix is stable across jobs.
	Messages []PiSeedMessage
	// Now is the timestamp base for the header and entries. Zero uses
	// time.Now(). Injectable so fixtures are deterministic.
	Now time.Time
}

// PiSessionSeedResult reports what was written.
type PiSessionSeedResult struct {
	// Path is the session file that was written.
	Path string
	// SessionID is the header id — the native session id groved records.
	SessionID string
	// Bytes is the size of the written file.
	Bytes int64
	// EntryIDs are the 8-hex-char ids of the appended entries, in order.
	EntryIDs []string
}

// piSeedEntryID derives an entry id from the session id and the entry's index.
// pi generates entry ids as randomUUID().slice(0, 8) and only requires
// uniqueness within the file, so a deterministic derivation is compatible AND
// strictly better here: the seed's bytes then depend only on its content and
// timestamps, which is what keeps a re-seed of identical content identical.
func piSeedEntryID(sessionID string, index int) string {
	return sha256Hex([]byte(fmt.Sprintf("%s:%d", sessionID, index)))[:8]
}

// piSessionIDPattern is pi's own assertValidSessionId regex
// (session-manager.ts): non-empty, alphanumerics plus '-', '_', '.', starting
// and ending alphanumeric. Enforced here because an id that fails it makes pi
// refuse the session at a later verb (/new, /branch) rather than at launch.
var piSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// piSeedSessionFileName builds the file name pi itself would choose for a
// session: the ISO timestamp with ':' and '.' replaced by '-', an underscore,
// then the session id. Matching it matters because grove reads the native
// session id back OUT of the filename (piNativeSessionID), and agentstream's
// discovery walks the same directory shape.
func piSeedSessionFileName(sessionID string, at time.Time) string {
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(at.UTC().Format(piSeedTimeLayout))
	return fmt.Sprintf("%s_%s.jsonl", stamp, sessionID)
}

// piSeedTimeLayout is JavaScript's Date.toISOString() layout: UTC, exactly three
// fractional digits, 'Z' suffix. pi writes every session timestamp this way.
const piSeedTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// piSeedTimestamp renders a timestamp the way pi does.
func piSeedTimestamp(at time.Time) string {
	return at.UTC().Format(piSeedTimeLayout)
}

// WritePiSessionSeed synthesizes a v3 session file at path.
//
// The file is written atomically (temp + rename) at 0600: it embeds the whole
// curated bundle, so a half-written seed must never be openable, and its bytes
// are as sensitive as the source it was rendered from.
func WritePiSessionSeed(path string, seed PiSessionSeed) (*PiSessionSeedResult, error) {
	if path == "" {
		return nil, fmt.Errorf("pi session seed: no output path")
	}
	if seed.CWD == "" {
		return nil, fmt.Errorf("pi session seed: no cwd — pi derives the agent's working directory from the session header, so it cannot be empty")
	}
	if len(seed.Messages) == 0 {
		return nil, fmt.Errorf("pi session seed: no in-context messages — an empty seed defeats the entire purpose of seeding")
	}

	now := seed.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	sessionID := seed.SessionID
	if sessionID == "" {
		generated, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("pi session seed: generating session id: %w", err)
		}
		sessionID = generated.String()
	}
	if !piSessionIDPattern.MatchString(sessionID) {
		return nil, fmt.Errorf("pi session seed: session id %q is not a valid pi session id (alphanumerics plus '-', '_', '.', starting and ending alphanumeric)", sessionID)
	}

	var buf bytes.Buffer
	writeLine := func(v any) error {
		// Entries are encoded one per line with HTML escaping OFF: the bundle
		// carries XML (`<file path=…>`), and Go's default escaping of <, > and &
		// would silently rewrite every byte of the oracle's context.
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}

	header := map[string]any{
		"type":      "session",
		"version":   piSessionFormatVersion,
		"id":        sessionID,
		"timestamp": piSeedTimestamp(now),
		"cwd":       seed.CWD,
	}
	if err := writeLine(header); err != nil {
		return nil, fmt.Errorf("pi session seed: encoding header: %w", err)
	}

	// Entries form a linear chain: entry N's parentId is entry N-1's id, and the
	// first entry's parentId is null. pi's _buildIndex takes the LAST entry as
	// the active leaf and walks parentId back to the root, so a broken chain
	// would silently drop everything above the break out of context.
	var (
		entryIDs []string
		parentID *string
		index    int
	)
	appendEntry := func(entry map[string]any) error {
		id := piSeedEntryID(sessionID, index)
		entry["id"] = id
		entry["parentId"] = parentID
		entry["timestamp"] = piSeedTimestamp(now.Add(time.Duration(index) * time.Millisecond))
		if err := writeLine(entry); err != nil {
			return err
		}
		entryIDs = append(entryIDs, id)
		held := id
		parentID = &held
		index++
		return nil
	}

	// The identity stamp rides FIRST and out of context. `custom` entries are
	// skipped by pi's sessionEntryToContextMessages, so this costs zero prompt
	// tokens while making the session self-describing to anything that reads it
	// later (the Phase 3 extension on reload, transcript tooling, a human).
	if seed.Stamp != nil {
		if err := appendEntry(map[string]any{
			"type":       "custom",
			"customType": PiSeedStampType,
			"data":       seed.Stamp,
		}); err != nil {
			return nil, fmt.Errorf("pi session seed: encoding identity stamp: %w", err)
		}
	}

	for _, msg := range seed.Messages {
		if msg.CustomType == "" {
			return nil, fmt.Errorf("pi session seed: in-context entry %d has no customType", index)
		}
		if strings.TrimSpace(msg.Content) == "" {
			return nil, fmt.Errorf("pi session seed: in-context entry %q has empty content", msg.CustomType)
		}
		if err := appendEntry(map[string]any{
			"type":       "custom_message",
			"customType": msg.CustomType,
			"content":    msg.Content,
			"display":    msg.Display,
		}); err != nil {
			return nil, fmt.Errorf("pi session seed: encoding %s entry: %w", msg.CustomType, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("pi session seed: creating session directory: %w", err)
	}
	if err := writeFileAtomic0600(path, buf.Bytes()); err != nil {
		return nil, fmt.Errorf("pi session seed: writing %s: %w", path, err)
	}

	return &PiSessionSeedResult{
		Path:      path,
		SessionID: sessionID,
		Bytes:     int64(buf.Len()),
		EntryIDs:  entryIDs,
	}, nil
}

// writeFileAtomic0600 writes content to path via a temp file in the same
// directory plus a rename, so a reader never observes a partial file.
func writeFileAtomic0600(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // no-op once the rename succeeded
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadPiSessionHeaderVersion reads the `version` of a pi session file's header
// line. It is the version probe: pointed at a session pi wrote ITSELF (any
// prior Pi-family job's transcript), it reports the format version the
// installed runtime currently emits, which is the only trustworthy signal that
// piSessionFormatVersion is still correct.
//
// Returns (0, nil) when the file has no parseable header — absence of evidence,
// not evidence of a mismatch, so callers advise rather than fail.
func ReadPiSessionHeaderVersion(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // Flow-owned session artifact
	if err != nil {
		return 0, err
	}
	line := data
	if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
		line = data[:idx]
	}
	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(line, &header); err != nil || header.Type != "session" {
		return 0, nil
	}
	return header.Version, nil
}

// ProbePiSessionFormat checks the newest pi-written session under sessionDir
// (excluding the seed itself) against piSessionFormatVersion. It returns a
// human-facing advisory string when they disagree, and "" when they agree or
// when there is nothing to compare against.
//
// Deliberately advisory, not a gate: the writer emits the CURRENT version and
// pi migrates older files forward, so the only real hazard is a future runtime
// whose newer version we cannot know about here. Surfacing that as a warning
// beside the launch is honest; refusing to launch on it would strand the user
// on a routine upgrade.
func ProbePiSessionFormat(sessionDir, excludePath string) string {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return ""
	}
	var (
		newestPath string
		newestAt   time.Time
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessionDir, entry.Name())
		if path == excludePath {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestAt) {
			newestAt = info.ModTime()
			newestPath = path
		}
	}
	if newestPath == "" {
		return ""
	}
	version, err := ReadPiSessionHeaderVersion(newestPath)
	if err != nil || version == 0 || version == piSessionFormatVersion {
		return ""
	}
	return fmt.Sprintf("pi wrote session version %d in %s but the Flow seed writer emits version %d — the seeded context may not load as expected; re-verify the seed format against the installed pi runtime",
		version, filepath.Base(newestPath), piSessionFormatVersion)
}
