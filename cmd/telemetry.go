package cmd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/spf13/cobra"
)

// This file implements `eka telemetry` (adr:038-telemetry-capture,
// spec:telemetry-capture:1): the append-only session event log that
// sto:compliance-telemetry (M5) consumes.
//
// Telemetry is a JSONL event log, one file per session at
// docs/telemetry/<sessionId>.jsonl in the repository working tree. It
// carries NO store-schema, corpus-token or CKO-envelope change: the
// builder lifts the *.jsonl files as source-only RSF attachments
// (ADR-027) and a clone receives them through the existing snapshot
// transport. Records are only appended; the file is never rewritten.
// Each record carries prevHash = SHA-256 of the previous record's raw
// line bytes ("" for the first), so the chain is tamper-evident with no
// server state.
//
// sessionId is opaque and client-supplied (--session, else the
// EKA_SESSION env var). EKA never mints-and-stores an id and never
// infers a session from timestamps; with neither source the record
// carries sessionId "" and sessionSource "unattributed" — honest
// degradation, never a fabricated id.
//
// Exit codes (the CLI 0/1/2 contract):
//
//	0  record appended / list rendered
//	1  a deterministic telemetry error (a broken prevHash chain, an
//	   unusable file, an unsupported record version, an IO failure)
//	2  usage error (missing/unknown --kind, an unknown --gate, a
//	   gate/kind mismatch, a malformed --since/--until timestamp)
//
// The read path is read-only and explicit: `eka get` / `eka context`
// never touch telemetry. The write and gate paths instrument opt-in
// only when a session id is present (see telemetryAutoEmit).

const (
	// telemetryRecordVersion is the schema version of one record (the
	// fixed "v" field; spec:telemetry-capture:1 §3).
	telemetryRecordVersion = 1
	// telemetryDirRel is the repository-relative telemetry directory.
	telemetryDirRel = "docs/telemetry"
	// telemetryFileExt is the per-session file extension.
	telemetryFileExt = ".jsonl"
	// telemetryEnvSession is the environment variable the host agent
	// exports to opt into auto-emit. Auto-emit runs only when it (or
	// --session) carries a non-empty session id.
	telemetryEnvSession = "EKA_SESSION"
)

// Telemetry record tokens (spec:telemetry-capture:1 §3). The ordered
// slices drive the usage-error messages; the sets drive validation.
const (
	telKindConstraintRead = "constraint-read"
	telKindFirstWrite     = "first-write"
	telKindWrite          = "write"
	telKindGateRefusal    = "gate-refusal"
	telKindGatePass       = "gate-pass"

	telGateConformance = "conformance"
	telGateTransition  = "transition"
	telGateVerify      = "verify"
	telGateNote        = "note"

	telSourceClient       = "client"
	telSourceEnv          = "env"
	telSourceUnattributed = "unattributed"
)

var (
	telemetryKinds   = []string{telKindConstraintRead, telKindFirstWrite, telKindWrite, telKindGateRefusal, telKindGatePass}
	telemetryGates   = []string{telGateConformance, telGateTransition, telGateVerify, telGateNote}
	telemetryKindSet = stringSet(telemetryKinds)
	telemetryGateSet = stringSet(telemetryGates)
)

// stringSet builds a membership set from a token list.
func stringSet(tokens []string) map[string]bool {
	m := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		m[t] = true
	}
	return m
}

// telemetryRecord is one telemetry event. Field order is fixed (the
// struct declaration order is the JSON serialization order) and matches
// spec:telemetry-capture:1 §3 exactly; every field is always present
// ("" when not applicable). It is the only shape M5 parses.
type telemetryRecord struct {
	V             int    `json:"v"`
	SessionID     string `json:"sessionId"`
	Seq           int    `json:"seq"`
	At            string `json:"at"`
	Repo          string `json:"repo"`
	Kind          string `json:"kind"`
	Subject       string `json:"subject"`
	Constraint    string `json:"constraint"`
	Gate          string `json:"gate"`
	Reason        string `json:"reason"`
	PrevHash      string `json:"prevHash"`
	SessionSource string `json:"sessionSource"`
}

// newTelemetryCommand builds the `eka telemetry` command tree: `emit`
// (append one record) and `list` (query records). This command is a
// thin Cobra layer over the append-only JSONL log — no store, no
// runtime, no id minting.
func newTelemetryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Append-only session telemetry event log (emit, list)",
		Long: `The EKA session telemetry event log (spec:telemetry-capture:1).

Telemetry is an append-only JSONL event log, one file per session at
docs/telemetry/<sessionId>.jsonl in the repository working tree. Each
line is one immutable record; the file is never rewritten. Every record
carries prevHash = SHA-256 of the previous record's raw bytes ("" for
the first), so the chain is tamper-evident with no server state.

sessionId is client-supplied: pass --session, or export EKA_SESSION.
EKA never mints-and-stores an id and never infers a session from
timestamps; with neither source the record carries sessionId "" and
sessionSource "unattributed".

Subcommands:
  emit  append one telemetry record
  list  query the telemetry records (per session/repo/period)

Exit codes:
  0  appended / rendered
  1  a deterministic telemetry error (broken chain, unusable file)
  2  usage error (missing/unknown --kind, unknown --gate, bad timestamp)`,
	}
	cmd.AddCommand(newTelemetryEmitCommand(), newTelemetryListCommand())
	return cmd
}

// newTelemetryEmitCommand builds `eka telemetry emit --kind <kind>
// [--session <id>] [--subject <form>] [--constraint <form>]
// [--gate <id>] [--reason <code>]`: it appends exactly one record to
// docs/telemetry/<sessionId>.jsonl, creating the directory and file as
// needed, and never rewrites an existing line.
//
// The emit command computes seq = last.seq + 1 (0 when the file is
// absent/empty) and prevHash = SHA-256 of the previous raw line. It
// validates the existing chain before appending: a file whose prevHash
// chain does not validate is a deterministic error (exit 1).
//
// Session resolution: --session ⇒ sessionSource "client"; otherwise a
// non-empty EKA_SESSION ⇒ sessionSource "env"; otherwise sessionId ""
// and sessionSource "unattributed" (two such runs produce two records,
// never one synthesized session).
//
// --kind is the canonical flag; --event is accepted as an alias (the
// spelling used by the ADR/spec prose). Exactly one is required.
//
// Exit codes:
//
//	0  record appended
//	1  a deterministic telemetry error (broken chain, IO failure)
//	2  usage error (missing/unknown --kind, unknown --gate, or a
//	   gate/kind mismatch: gate-* kinds require --gate, others forbid it)
func newTelemetryEmitCommand() *cobra.Command {
	var session, kind, event, subject, constraint, gate, reason string
	cmd := &cobra.Command{
		Use:   "emit --kind <kind> [--session <id>] [--subject <form>] [--constraint <form>] [--gate <id>] [--reason <code>]",
		Short: "Append one telemetry record to the session log",
		Long: `Append one telemetry record to docs/telemetry/<sessionId>.jsonl.

The record carries a fixed field order and a prevHash chain: seq is the
only ordering key (last.seq + 1, 0 for a new file) and prevHash is the
SHA-256 of the previous record's raw bytes ("" for the first). A file
whose chain does not validate is a deterministic error — the log is
tamper-evident with no server state.

Kinds:
  constraint-read   a constraint was read before the first write
  first-write       the session's first write
  write             a subsequent write
  gate-refusal      a gate refused (gate + reason)
  gate-pass         a gate passed (gate)

Gates (for gate-* kinds): conformance | transition | verify | note
Rule: gate-refusal/gate-pass MUST carry --gate; every other kind MUST NOT.

Session:
  --session <id>    the client-supplied session id (sessionSource client)
  EKA_SESSION env   used when --session is absent (sessionSource env)
  neither           sessionId "" and sessionSource "unattributed"

Concurrency: emit assumes a single writer per session file (no locking).
The session id is client-owned, so the client serialises its own writes;
two concurrent emits for one session id can duplicate seq and break the
chain. A broken chain makes further explicit emits fail (exit 1) and
further auto-emits no-op silently — acceptable, telemetry is auxiliary
and M5 reports coverage, so a gap is visible rather than masked.

Exit codes:
  0  record appended
  1  deterministic telemetry error (broken chain, IO failure)
  2  usage error (missing/unknown --kind, unknown --gate, gate/kind mismatch)`,
		Example: `  eka telemetry emit --kind write --session 01J8Z...
  eka telemetry emit --kind gate-refusal --session s1 --gate transition --subject sto:12 --reason "illegal transition"
  eka telemetry emit --kind constraint-read --subject eka/adr:011-... --constraint eka/adr:011-...`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			k := kind
			switch {
			case kind != "" && event != "" && kind != event:
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: telemetry emit: --kind and --event disagree; pass one")
				return &exitError{code: exitUsage}
			case k == "":
				k = event
			}
			return runTelemetryEmit(cmd, session, k, subject, constraint, gate, reason)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "event kind: "+strings.Join(telemetryKinds, "|"))
	cmd.Flags().StringVar(&event, "event", "", "alias of --kind (the spec prose spelling)")
	cmd.Flags().StringVar(&session, "session", "", "client-supplied session id (default: EKA_SESSION; else unattributed)")
	cmd.Flags().StringVar(&subject, "subject", "", "the record subject (canonical or line form; default empty)")
	cmd.Flags().StringVar(&constraint, "constraint", "", "the constraint read/refused (canonical or line form; default empty)")
	cmd.Flags().StringVar(&gate, "gate", "", "the gate id (required for gate-* kinds, forbidden otherwise): "+strings.Join(telemetryGates, "|"))
	cmd.Flags().StringVar(&reason, "reason", "", "the deterministic refusal string (gate-refusal)")
	return cmd
}

// runTelemetryEmit resolves the session id and its source, resolves the
// repository identity, and appends one validated record. A usage-class
// refusal exits 2; a deterministic telemetry error exits 1.
func runTelemetryEmit(cmd *cobra.Command, session, kind, subject, constraint, gate, reason string) error {
	if !telemetryKindSet[kind] {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry emit: --kind is required and must be one of %s\n", strings.Join(telemetryKinds, "|"))
		return &exitError{code: exitUsage}
	}
	if gate != "" && !telemetryGateSet[gate] {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry emit: --gate must be one of %s\n", strings.Join(telemetryGates, "|"))
		return &exitError{code: exitUsage}
	}
	// Gate <-> kind rule (M5 keys coverage on gate-bearing events): the
	// gate-* kinds must carry a gate, every other kind must not.
	isGateKind := kind == telKindGateRefusal || kind == telKindGatePass
	switch {
	case isGateKind && gate == "":
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry emit: --kind %s requires a --gate (%s)\n", kind, strings.Join(telemetryGates, "|"))
		return &exitError{code: exitUsage}
	case !isGateKind && gate != "":
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry emit: --kind %s must not carry a --gate\n", kind)
		return &exitError{code: exitUsage}
	}
	source := telSourceUnattributed
	switch {
	case session != "":
		source = telSourceClient
	case os.Getenv(telemetryEnvSession) != "":
		session, source = os.Getenv(telemetryEnvSession), telSourceEnv
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry: %v\n", err)
		return &exitError{code: exitFail}
	}
	root, repo, err := telemetryRoot(cwd)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry: %v\n", err)
		return &exitError{code: exitFail}
	}
	rec, err := appendTelemetry(root, repo, session, source, kind, subject, constraint, gate, reason)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: %v\n", err)
		return &exitError{code: exitFail}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "telemetry: %s seq=%d kind=%s\n", telemetrySessionLabel(rec.SessionID), rec.Seq, rec.Kind)
	return nil
}

// newTelemetryListCommand builds `eka telemetry list [--session <id>]
// [--repo <id>] [--since <t>] [--until <t>] [--json]`: it prints records
// sorted by (sessionId, seq). The JSON form is JSONL — one canonical
// record object per line — deterministic byte-for-byte for a fixed file
// state. Files are streamed one at a time (the whole set is never held
// in memory); a malformed line is a deterministic error naming the file
// and its line number, never silently skipped. A missing telemetry
// directory is an empty result (exit 0).
//
// Exit codes:
//
//	0  records rendered (possibly zero)
//	1  a deterministic telemetry error (malformed line, broken chain)
//	2  usage error (a malformed --since/--until timestamp)
func newTelemetryListCommand() *cobra.Command {
	var session, repo, since, until string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list [--session <id>] [--repo <id>] [--since <t>] [--until <t>] [--json]",
		Short: "Query the telemetry records (sorted by sessionId, seq)",
		Long: `Query the telemetry records of the current repository, sorted by
(sessionId, seq) — deterministic for a fixed file state.

Filters:
  --session <id>   only the named session's file
  --repo <id>      only records whose repo equals <id> (e.g. eka/eka-cli)
  --since <t>      only records with at >= <t> (RFC3339)
  --until <t>      only records with at <= <t> (RFC3339)

--json emits one canonical record object per line (JSONL), byte-stable
across runs. The reader streams the JSONL files; a malformed line is a
deterministic error naming the file and line, never silently skipped.

Exit codes:
  0  records rendered (possibly zero)
  1  deterministic telemetry error (malformed line, broken chain)
  2  usage error (a malformed --since/--until timestamp)`,
		Example: `  eka telemetry list
  eka telemetry list --session 01J8Z... --json
  eka telemetry list --repo eka/eka-cli --since 2026-11-01T00:00:00Z --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceT, err := parseTelemetryTime("--since", since)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry list: %v\n", err)
				return &exitError{code: exitUsage}
			}
			untilT, err := parseTelemetryTime("--until", until)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry list: %v\n", err)
				return &exitError{code: exitUsage}
			}
			return runTelemetryList(cmd, session, repo, sinceT, untilT, asJSON)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "only the named session's records")
	cmd.Flags().StringVar(&repo, "repo", "", "only records whose repo equals <id> (e.g. eka/eka-cli)")
	cmd.Flags().StringVar(&since, "since", "", "only records with at >= <t> (RFC3339)")
	cmd.Flags().StringVar(&until, "until", "", "only records with at <= <t> (RFC3339)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit one canonical record object per line (JSONL)")
	return cmd
}

// parseTelemetryTime parses an optional RFC3339 filter value. An empty
// string yields the zero time and no error.
func parseTelemetryTime(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not an RFC3339 timestamp", flag, value)
	}
	return t, nil
}

// runTelemetryList streams the session files (sorted by session id, so
// the (sessionId, seq) order holds because each file is in seq order)
// and applies the filters. No store, no runtime.
func runTelemetryList(cmd *cobra.Command, session, repo string, since, until time.Time, asJSON bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry: %v\n", err)
		return &exitError{code: exitFail}
	}
	root, _, err := telemetryRoot(cwd)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry: %v\n", err)
		return &exitError{code: exitFail}
	}
	dir := telemetryDirPath(root)

	type sessionFile struct {
		session string
		path    string
	}
	var files []sessionFile
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil:
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), telemetryFileExt) {
				continue
			}
			sid := strings.TrimSuffix(e.Name(), telemetryFileExt)
			if session != "" && sid != session {
				continue
			}
			files = append(files, sessionFile{session: sid, path: filepath.Join(dir, e.Name())})
		}
	case errors.Is(err, os.ErrNotExist):
		// No telemetry yet: empty result.
	default:
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: telemetry: cannot read %s: %v\n", dir, err)
		return &exitError{code: exitFail}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].session < files[j].session })

	s := styleFor(cmd)
	w := cmd.OutOrStdout()
	if !asJSON {
		renderTelemetryListHeader(s, root, session, repo, since, until)
	}

	count := 0
	for _, f := range files {
		err := scanTelemetry(f.path, func(rec telemetryRecord) error {
			if repo != "" && rec.Repo != repo {
				return nil
			}
			if !since.IsZero() || !until.IsZero() {
				at, perr := time.Parse(time.RFC3339, rec.At)
				if perr != nil {
					return fmt.Errorf("telemetry: %s: record seq %d carries an invalid at timestamp %q", f.path, rec.Seq, rec.At)
				}
				if !since.IsZero() && at.Before(since) {
					return nil
				}
				if !until.IsZero() && at.After(until) {
					return nil
				}
			}
			count++
			return writeTelemetryRecord(w, s, rec, asJSON)
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: %v\n", err)
			return &exitError{code: exitFail}
		}
	}
	if !asJSON && count == 0 {
		fmt.Fprintf(s.W, "  (no records)\n")
	}
	return nil
}

// renderTelemetryListHeader prints the human context header for the
// list (mirrors verify's deterministic non-TTY style).
func renderTelemetryListHeader(s *ui.Style, root, session, repo string, since, until time.Time) {
	h := ui.NewHeader(s, "Telemetry").Add("Root", root)
	if session != "" {
		h.Add("Session", session)
	}
	if repo != "" {
		h.Add("Repo", repo)
	}
	if !since.IsZero() {
		h.Add("Since", since.Format(time.RFC3339))
	}
	if !until.IsZero() {
		h.Add("Until", until.Format(time.RFC3339))
	}
	h.Pipeline("List").Render()
	fmt.Fprintf(s.W, "\nRecords:\n")
}

// writeTelemetryRecord writes one record: the canonical JSON object plus
// a newline with --json, else a readable one-line summary.
func writeTelemetryRecord(w io.Writer, s *ui.Style, rec telemetryRecord, asJSON bool) error {
	if asJSON {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("telemetry: cannot encode record: %w", err)
		}
		_, err = w.Write(append(line, '\n'))
		return err
	}
	fmt.Fprintf(w, "  %s %s\n", s.Info(telemetrySessionLabel(rec.SessionID)), telemetryRecordSummary(rec))
	return nil
}

// telemetryRecordSummary renders the non-identity fields of one record
// as a deterministic one-line summary.
func telemetryRecordSummary(rec telemetryRecord) string {
	parts := []string{fmt.Sprintf("#%d", rec.Seq), rec.Kind}
	if rec.Gate != "" {
		parts = append(parts, "gate="+rec.Gate)
	}
	if rec.Subject != "" {
		parts = append(parts, "subject="+rec.Subject)
	}
	if rec.Constraint != "" {
		parts = append(parts, "constraint="+rec.Constraint)
	}
	if rec.Reason != "" {
		parts = append(parts, "reason="+rec.Reason)
	}
	return strings.Join(parts, " ")
}

// telemetrySessionLabel renders a session id for human output, naming
// the unattributed case explicitly (an empty id is not a session).
func telemetrySessionLabel(session string) string {
	if session == "" {
		return "(unattributed)"
	}
	return session
}

// telemetryRoot resolves the repository root and the <project>/<name>
// repository identity from cwd through the ADR-018 eka.yaml walk-up
// (shared with snapshotRepositoryRoot). Outside an EKA repository the
// root is cwd and the identity is empty (records carry repo "").
func telemetryRoot(cwd string) (root, repo string, err error) {
	meta, mdir, hasMeta, err := metadata.Find(cwd)
	if err != nil {
		return "", "", err
	}
	if !hasMeta {
		abs, aerr := filepath.Abs(cwd)
		if aerr != nil {
			return "", "", aerr
		}
		return filepath.Clean(abs), "", nil
	}
	return mdir, meta.Project + "/" + meta.Name, nil
}

// telemetryDirPath is the telemetry directory under root.
func telemetryDirPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(telemetryDirRel))
}

// telemetryFilePath is the per-session file under root. With an empty
// session id the name is literally ".jsonl" (the spec's
// <sessionId>.jsonl with an empty id); list discovers it through
// os.ReadDir, which unlike filepath.Glob sees dotfiles.
//
// ponytail: a single per-session file read whole for append is O(n) per
// emit; upgrade to an O(1) tail-read if a session ever grows very large.
func telemetryFilePath(root, session string) string {
	return filepath.Join(telemetryDirPath(root), session+telemetryFileExt)
}

// appendTelemetry validates the existing chain, computes seq/prevHash
// and appends exactly one record (creating the directory/file). It
// returns the appended record. A broken chain or IO failure is an
// error; the file is never rewritten.
//
// ponytail: assumes a single writer per session file — there is no
// locking. Two concurrent appends to the same session id can duplicate
// seq and break the chain; the session id is client-owned, so the
// client serialises its own writes. Add platform file locking only if a
// real multi-writer-per-session case appears.
func appendTelemetry(root, repo, session, source, kind, subject, constraint, gate, reason string) (telemetryRecord, error) {
	path := telemetryFilePath(root, session)
	nextSeq, prevHash, err := telemetryAppendPoint(path)
	if err != nil {
		return telemetryRecord{}, err
	}
	rec := telemetryRecord{
		V:             telemetryRecordVersion,
		SessionID:     session,
		Seq:           nextSeq,
		At:            time.Now().UTC().Format(time.RFC3339),
		Repo:          repo,
		Kind:          kind,
		Subject:       subject,
		Constraint:    constraint,
		Gate:          gate,
		Reason:        reason,
		PrevHash:      prevHash,
		SessionSource: source,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return telemetryRecord{}, fmt.Errorf("telemetry: cannot encode record: %w", err)
	}
	if err := os.MkdirAll(telemetryDirPath(root), 0o755); err != nil {
		return telemetryRecord{}, fmt.Errorf("telemetry: cannot create %s: %w", telemetryDirPath(root), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return telemetryRecord{}, fmt.Errorf("telemetry: cannot open %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return telemetryRecord{}, fmt.Errorf("telemetry: cannot append to %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return telemetryRecord{}, fmt.Errorf("telemetry: cannot close %s: %w", path, err)
	}
	return rec, nil
}

// telemetryAppendPoint validates the append-only chain of path and
// returns the next seq (last.seq + 1, 0 when absent/empty) and the hash
// of the last raw line ("" when absent/empty) for the next prevHash. A
// missing file is not an error. A file that does not end with a newline
// is a deterministic error (the emitter always terminates every record).
func telemetryAppendPoint(path string) (nextSeq int, prevHash string, err error) {
	data, rerr := os.ReadFile(path)
	if errors.Is(rerr, os.ErrNotExist) {
		return 0, "", nil
	}
	if rerr != nil {
		return 0, "", fmt.Errorf("telemetry: cannot read %s: %w", path, rerr)
	}
	// Deliberate: the read-only scan tolerates a final record without a
	// trailing newline, but append must not — appending after it would
	// concatenate two JSON objects onto one line and corrupt the file.
	// So append is intentionally stricter than scanTelemetry.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return 0, "", fmt.Errorf("telemetry: %s does not end with a newline (every record is appended with one)", path)
	}
	v := &telemetryChainValidator{path: path}
	rest := data
	for len(rest) > 0 {
		var line []byte
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			line, rest = rest, nil
		}
		// bufio.ScanLines (scanTelemetry) strips a trailing "\r" from
		// every line, so strip it here too: both readers must hash
		// identical bytes. This also keeps the chain stable across an
		// LF<->CRLF checkout, since git rewrites the bytes on checkout
		// and the hash is taken over the on-disk bytes either way.
		line = bytes.TrimSuffix(line, []byte("\r"))
		rec, cerr := v.check(line)
		if cerr != nil {
			return 0, "", cerr
		}
		nextSeq = rec.Seq + 1
	}
	return nextSeq, v.prevHash, nil
}

// scanTelemetry streams one session file line by line, validating the
// append-only chain (tamper-evidence) and calling visit for every
// record in file order. A missing file returns os.ErrNotExist. A
// malformed line, a broken prevHash chain or a non-increasing seq is a
// deterministic error naming the file and 1-based line.
func scanTelemetry(path string, visit func(telemetryRecord) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	v := &telemetryChainValidator{path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		rec, err := v.check(sc.Bytes())
		if err != nil {
			return err
		}
		if visit != nil {
			if err := visit(rec); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("telemetry: %s: %w", path, err)
	}
	return nil
}

// telemetryChainValidator carries the chain state across the lines of
// one file: the line counter, the previous raw line's hash and seq. The
// first record's prevHash must be empty; every later record's prevHash
// must equal the previous raw line's SHA-256 and its seq must strictly
// increase. This is the whole tamper-evidence contract, with no server
// state.
type telemetryChainValidator struct {
	path     string
	line     int
	prevHash string
	prevSeq  int
}

// check validates one raw line and returns its decoded record.
func (v *telemetryChainValidator) check(raw []byte) (telemetryRecord, error) {
	v.line++
	rec, err := decodeTelemetryRecord(raw)
	if err != nil {
		return telemetryRecord{}, fmt.Errorf("telemetry: %s:%d: %v", v.path, v.line, err)
	}
	if v.line == 1 {
		if rec.PrevHash != "" {
			return telemetryRecord{}, fmt.Errorf("telemetry: %s:%d: the first record's prevHash must be empty", v.path, v.line)
		}
	} else {
		if rec.PrevHash != v.prevHash {
			return telemetryRecord{}, fmt.Errorf("telemetry: %s:%d: chain broken: prevHash does not match the previous record's bytes", v.path, v.line)
		}
		if rec.Seq <= v.prevSeq {
			return telemetryRecord{}, fmt.Errorf("telemetry: %s:%d: seq %d is not greater than the previous seq %d", v.path, v.line, rec.Seq, v.prevSeq)
		}
	}
	v.prevHash = telemetryHash(raw)
	v.prevSeq = rec.Seq
	return rec, nil
}

// decodeTelemetryRecord strictly decodes one raw JSONL line into a
// record (unknown fields are refused — the schema is versioned by "v").
func decodeTelemetryRecord(raw []byte) (telemetryRecord, error) {
	var rec telemetryRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return telemetryRecord{}, fmt.Errorf("malformed record: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return telemetryRecord{}, errors.New("malformed record: trailing data after the JSON object")
	}
	// The schema is versioned by "v": a record at an unknown version cannot
	// be interpreted, so it is refused deterministically (the validator
	// wraps this with the path:line).
	if rec.V != telemetryRecordVersion {
		return telemetryRecord{}, fmt.Errorf("malformed record: unsupported version %d", rec.V)
	}
	return rec, nil
}

// telemetryHash is the hex SHA-256 of a raw line's bytes (the prevHash
// input; the trailing newline is never part of it).
func telemetryHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// telemetrySessionHasWrite reports whether the session log already
// carries a write record (first-write or write). A missing or
// unreadable file is treated as "no prior write" (the caller's append
// then surfaces any real error).
func telemetrySessionHasWrite(path string) bool {
	has := false
	err := scanTelemetry(path, func(rec telemetryRecord) error {
		if rec.Kind == telKindFirstWrite || rec.Kind == telKindWrite {
			has = true
		}
		return nil
	})
	if err != nil {
		return false
	}
	return has
}

// telemetryAutoEmit is the opt-in instrumentation of the write and gate
// paths. It is a no-op unless a session id is present (EKA_SESSION), so
// a plain CLI run writes nothing. When it does run it is best-effort:
// every error is swallowed silently (the host command's exit code and
// output are never changed by telemetry). For kind write it upgrades to
// first-write when the session has no prior write record.
func telemetryAutoEmit(kind, subject, constraint, gate, reason string) {
	session := os.Getenv(telemetryEnvSession)
	if session == "" {
		return // opt-in: no session id, no write.
	}
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root, repo, err := telemetryRoot(cwd)
	if err != nil {
		return
	}
	if kind == telKindWrite && !telemetrySessionHasWrite(telemetryFilePath(root, session)) {
		kind = telKindFirstWrite
	}
	_, _ = appendTelemetry(root, repo, session, telSourceEnv, kind, subject, constraint, gate, reason)
}
