package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/v2/exchange"
)

// cmd tests for `eka telemetry` (Wi-A emit + Wi-B list). Fixtures build
// a repository with eka.yaml (or reuse the shared store-seeded repos)
// and drive the command through runIn; the append-only chain, the
// client-supplied session semantics, the opt-in auto-emit on the write
// and gate paths, the read-only read path and the source-only
// attachment round-trip are all asserted at CLI level.

// telemetryRepo creates an EKA repository (eka.yaml: demo/app) in a temp
// dir and chdirs into it. No workspace is needed: telemetry touches
// neither the store nor the runtime.
func telemetryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEkaYAML(t, dir, "demo", "app", "demo")
	t.Chdir(dir)
	return dir
}

// telemetryPath is the per-session file path under a repository root.
func telemetryPath(dir, session string) string {
	return filepath.Join(dir, "docs", "telemetry", session+".jsonl")
}

// telemetryLines returns the raw lines of a telemetry file (no trailing
// newline; the file is newline-terminated).
func telemetryLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// telemetryRecords decodes every line of a telemetry file.
func telemetryRecords(t *testing.T, path string) []telemetryRecord {
	t.Helper()
	lines := telemetryLines(t, path)
	recs := make([]telemetryRecord, 0, len(lines))
	for _, line := range lines {
		var rec telemetryRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, line)
		}
		recs = append(recs, rec)
	}
	return recs
}

// writeTelemetryFile writes a valid chain for the given records (the
// helper fills v, sessionId, seq and prevHash so callers only describe
// the meaningful fields).
func writeTelemetryFile(t *testing.T, dir, session string, recs []telemetryRecord) string {
	t.Helper()
	path := telemetryPath(dir, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	prev := ""
	for i, rec := range recs {
		rec.V = telemetryRecordVersion
		rec.SessionID = session
		rec.Seq = i
		rec.PrevHash = prev
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		prev = telemetryHash(line)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeTelemetryFileCRLF writes the same valid chain as
// writeTelemetryFile but with CRLF line endings, emulating a checkout
// with core.autocrlf=true. The prevHash of every record is the hash of
// the line WITHOUT the trailing "\r" (the line the emitter hashes);
// both readers strip the "\r" before hashing, so the chain stays valid.
func writeTelemetryFileCRLF(t *testing.T, dir, session string, recs []telemetryRecord) string {
	t.Helper()
	path := telemetryPath(dir, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	prev := ""
	for i, rec := range recs {
		rec.V = telemetryRecordVersion
		rec.SessionID = session
		rec.Seq = i
		rec.PrevHash = prev
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteString("\r\n")
		prev = telemetryHash(line)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// listJSONLines runs `eka telemetry list --json` and returns the decoded
// records (one per line).
func listJSONLines(t *testing.T, args ...string) []telemetryRecord {
	t.Helper()
	code, out, errText := runIn(append([]string{"telemetry", "list", "--json"}, args...))
	if code != 0 {
		t.Fatalf("list %v: exit = %d\nstderr: %s", args, code, errText)
	}
	var recs []telemetryRecord
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec telemetryRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode list line: %v\n%s", err, line)
		}
		recs = append(recs, rec)
	}
	return recs
}

// --- Wi-A: emit -------------------------------------------------------

// TestTelemetryEmitAppendsAndChains: emit appends one line per run, the
// record carries the fixed field order and the prevHash chain validates.
func TestTelemetryEmitAppendsAndChains(t *testing.T) {
	dir := telemetryRepo(t)
	if code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"}); code != 0 {
		t.Fatalf("emit 1: exit = %d\n%s", code, errText)
	}
	if code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "gate-pass", "--session", "s1", "--gate", "verify"}); code != 0 {
		t.Fatalf("emit 2: exit = %d\n%s", code, errText)
	}
	path := telemetryPath(dir, "s1")
	lines := telemetryLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	// Fixed field order: the raw line starts with the v/sessionId/seq/at prefix.
	if !strings.HasPrefix(lines[0], `{"v":1,"sessionId":"s1","seq":0,"at":"`) {
		t.Errorf("record field order wrong:\n%s", lines[0])
	}
	// The chain validates through the reader.
	if code, _, errText := runIn([]string{"telemetry", "list", "--session", "s1"}); code != 0 {
		t.Fatalf("list: exit = %d\n%s", code, errText)
	}
	recs := telemetryRecords(t, path)
	if recs[0].SessionID != "s1" || recs[0].Seq != 0 || recs[0].Kind != "write" ||
		recs[0].Repo != "demo/app" || recs[0].SessionSource != telSourceClient || recs[0].PrevHash != "" {
		t.Errorf("record 0 = %+v", recs[0])
	}
	if recs[1].Seq != 1 || recs[1].Kind != telKindGatePass || recs[1].Gate != "verify" {
		t.Errorf("record 1 = %+v", recs[1])
	}
	if recs[1].PrevHash != telemetryHash([]byte(lines[0])) {
		t.Errorf("prevHash = %q, want hash of the previous raw line", recs[1].PrevHash)
	}
	if _, err := time.Parse(time.RFC3339, recs[0].At); err != nil {
		t.Errorf("at %q is not RFC3339: %v", recs[0].At, err)
	}
	// The file is never rewritten: a third append keeps the first two lines.
	before := strings.Join(lines, "\n")
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"}); code != 0 {
		t.Fatal("emit 3 failed")
	}
	after := strings.Join(telemetryLines(t, path)[:2], "\n")
	if before != after {
		t.Errorf("existing lines were rewritten:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestTelemetryEmitChainTamperFails: a file whose prevHash chain does
// not validate is a deterministic error; fixing it restores success
// (non-vacuity).
func TestTelemetryEmitChainTamperFails(t *testing.T) {
	dir := telemetryRepo(t)
	path := writeTelemetryFile(t, dir, "s1", []telemetryRecord{{Kind: "write"}, {Kind: "write"}})

	// Tamper line 0's bytes, leaving line 1's prevHash pointing at the
	// ORIGINAL line 0 → the chain breaks at line 2.
	lines := telemetryLines(t, path)
	lines[0] = strings.Replace(lines[0], `"kind":"write"`, `"kind":"gate-pass"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"})
	if code != 1 {
		t.Fatalf("tampered emit: exit = %d, want 1\n%s", code, errText)
	}
	if !strings.Contains(errText, "chain broken") || !strings.Contains(errText, path+":2") {
		t.Errorf("tampered emit stderr must name the file:line and the broken chain, got %q", errText)
	}

	// Non-vacuity: a valid file emits cleanly.
	writeTelemetryFile(t, dir, "s1", []telemetryRecord{{Kind: "write"}, {Kind: "write"}})
	if code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"}); code != 0 {
		t.Fatalf("valid emit: exit = %d, want 0\n%s", code, errText)
	}
}

// TestTelemetryCRLFChain: a CRLF-normalised checkout (core.autocrlf=true)
// does not break the chain. Both readers strip the trailing "\r" before
// hashing, so emit appends cleanly and list agrees. Non-vacuity: without
// the strip, append hashes "...}\r" while the committed prevHash was
// computed over "...}", a false "chain broken".
func TestTelemetryCRLFChain(t *testing.T) {
	dir := telemetryRepo(t)
	path := writeTelemetryFileCRLF(t, dir, "s1", []telemetryRecord{{Kind: "write"}, {Kind: "write"}})

	// The reader accepts the CRLF chain.
	if code, _, errText := runIn([]string{"telemetry", "list", "--json"}); code != 0 {
		t.Fatalf("list over CRLF: exit = %d\n%s", code, errText)
	}

	// The appender accepts it and continues the chain at seq 2.
	if code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"}); code != 0 {
		t.Fatalf("emit over CRLF: exit = %d, want 0\n%s", code, errText)
	}
	recs := telemetryRecords(t, path)
	if len(recs) != 3 || recs[2].Seq != 2 {
		t.Fatalf("records = %d, want 3 ending at seq 2: %+v", len(recs), recs)
	}
	// The appended line is LF (the emitter writes LF); the chain still
	// validates because the reader strips CR before hashing.
	if code, _, errText := runIn([]string{"telemetry", "list", "--json"}); code != 0 {
		t.Fatalf("list after append: exit = %d\n%s", code, errText)
	}
}

// TestTelemetryEmitUnattributed: with no session id the record carries
// sessionId "" and sessionSource "unattributed"; two runs produce two
// records, never a synthesized session.
func TestTelemetryEmitUnattributed(t *testing.T) {
	dir := telemetryRepo(t)
	t.Setenv(telemetryEnvSession, "")
	for i := 0; i < 2; i++ {
		if code, _, errText := runIn([]string{"telemetry", "emit", "--kind", "write"}); code != 0 {
			t.Fatalf("emit %d: exit = %d\n%s", i, code, errText)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "docs", "telemetry"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("telemetry files = %d, want 1 (the unattributed log)", len(entries))
	}
	recs := telemetryRecords(t, telemetryPath(dir, ""))
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	for i, rec := range recs {
		if rec.SessionID != "" || rec.SessionSource != telSourceUnattributed || rec.Seq != i {
			t.Errorf("record %d = %+v, want empty sessionId + unattributed, seq %d", i, rec, i)
		}
	}
}

// TestTelemetryEmitUsage: an unknown kind or gate is a usage error (2),
// and --event is accepted as the spec-prose alias of --kind.
func TestTelemetryEmitUsage(t *testing.T) {
	telemetryRepo(t)
	if code, _, _ := runIn([]string{"telemetry", "emit", "--session", "s1"}); code != 2 {
		t.Errorf("missing --kind: exit = %d, want 2", code)
	}
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "bogus", "--session", "s1"}); code != 2 {
		t.Errorf("unknown --kind: exit = %d, want 2", code)
	}
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "gate-pass", "--gate", "bogus"}); code != 2 {
		t.Errorf("unknown --gate: exit = %d, want 2", code)
	}
	// --event alias works.
	if code, _, errText := runIn([]string{"telemetry", "emit", "--event", "write", "--session", "s1"}); code != 0 {
		t.Errorf("--event alias: exit = %d\n%s", code, errText)
	}
	// Disagreeing --kind / --event is a usage error.
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "write", "--event", "gate-pass", "--session", "s1"}); code != 2 {
		t.Errorf("disagreeing --kind/--event: exit = %d, want 2", code)
	}
}

// TestTelemetryEmitGateKind: the gate <-> kind rule (M5 keys coverage on
// gate-bearing events): gate-* kinds REQUIRE --gate, every other kind
// FORBIDS it. Non-vacuity: the valid case exits 0, so the checks do not
// over-reject.
func TestTelemetryEmitGateKind(t *testing.T) {
	dir := telemetryRepo(t)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"write with a gate", []string{"--kind", "write", "--gate", "verify"}, 2},
		{"gate-refusal without a gate", []string{"--kind", "gate-refusal"}, 2},
		{"gate-pass without a gate", []string{"--kind", "gate-pass"}, 2},
		{"gate-refusal with a gate", []string{"--kind", "gate-refusal", "--gate", "transition", "--subject", "sto:1"}, 0},
		{"write without a gate", []string{"--kind", "write"}, 0},
		{"constraint-read without a gate", []string{"--kind", "constraint-read"}, 0},
	}
	for _, tc := range cases {
		args := append([]string{"telemetry", "emit", "--session", "s"}, tc.args...)
		if code, _, errText := runIn(args); code != tc.want {
			t.Errorf("%s: exit = %d, want %d\n%s", tc.name, code, tc.want, errText)
		}
	}
	// The three exit-0 cases landed, in order, with their gate (or none).
	recs := telemetryRecords(t, telemetryPath(dir, "s"))
	if len(recs) != 3 {
		t.Fatalf("records = %d, want 3 (the three exit-0 cases): %+v", len(recs), recs)
	}
	if recs[0].Kind != telKindGateRefusal || recs[0].Gate != telGateTransition {
		t.Errorf("record 0 = %+v, want gate-refusal/transition", recs[0])
	}
	if recs[1].Kind != telKindWrite || recs[1].Gate != "" {
		t.Errorf("record 1 = %+v, want write with no gate", recs[1])
	}
	if recs[2].Kind != telKindConstraintRead || recs[2].Gate != "" {
		t.Errorf("record 2 = %+v, want constraint-read with no gate", recs[2])
	}
}

// TestTelemetryListUnattributedFile: an unattributed session file is
// literally ".jsonl"; list discovers it through os.ReadDir (which, unlike
// filepath.Glob, sees dotfiles) and renders its records.
func TestTelemetryListUnattributedFile(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "", []telemetryRecord{
		{Repo: "demo/app", Kind: "write", At: "2026-11-22T00:00:00Z"},
		{Repo: "demo/app", Kind: "write", At: "2026-11-22T00:00:01Z"},
	})
	if _, err := os.Stat(telemetryPath(dir, "")); err != nil {
		t.Fatalf("unattributed file .jsonl missing: %v", err)
	}
	recs := listJSONLines(t)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 (the dotfile session): %+v", len(recs), recs)
	}
	for i, rec := range recs {
		if rec.SessionID != "" || rec.Seq != i {
			t.Errorf("record %d = %+v, want sessionId \"\" seq %d", i, rec, i)
		}
	}
}

// TestTelemetryListUnsupportedVersion: a well-formed record at an unknown
// schema version is refused deterministically, naming the file and line.
// Non-vacuity: the identical record at v=1 lists cleanly.
func TestTelemetryListUnsupportedVersion(t *testing.T) {
	dir := telemetryRepo(t)
	path := telemetryPath(dir, "s1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const tmpl = `{"v":%d,"sessionId":"s1","seq":0,"at":"2026-11-22T00:00:00Z","repo":"demo/app","kind":"write","subject":"","constraint":"","gate":"","reason":"","prevHash":"","sessionSource":"client"}`
	if err := os.WriteFile(path, []byte(fmt.Sprintf(tmpl, 2)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"telemetry", "list"})
	if code != 1 {
		t.Fatalf("unsupported version: exit = %d, want 1\n%s", code, errText)
	}
	if !strings.Contains(errText, path+":1") || !strings.Contains(errText, "unsupported version 2") {
		t.Errorf("stderr must name the file:line and the version, got %q", errText)
	}

	// Non-vacuity: the same record at the supported version lists cleanly.
	if err := os.WriteFile(path, []byte(fmt.Sprintf(tmpl, 1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := runIn([]string{"telemetry", "list"}); code != 0 {
		t.Fatalf("supported version: exit = %d, want 0\n%s", code, errText)
	}
}

// --- Wi-A: auto-emit --------------------------------------------------

// TestTelemetryAutoEmitTransitionRefusal: an opted-in transition refusal
// records a gate-refusal (gate transition) with the subject and reason,
// and the host command's exit code and stderr are unchanged.
func TestTelemetryAutoEmitTransitionRefusal(t *testing.T) {
	transitionEnv(t)
	t.Setenv(telemetryEnvSession, "sess-t")
	code, _, errText := runIn([]string{"transition", "sto:one", "planned"})
	if code != 1 || !strings.Contains(errText, "transition refused") {
		t.Fatalf("refusal: exit = %d, stderr = %q", code, errText)
	}
	dir, _ := os.Getwd()
	recs := telemetryRecords(t, telemetryPath(dir, "sess-t"))
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Kind != telKindGateRefusal || rec.Gate != telGateTransition || rec.Subject != "sto:one" ||
		rec.SessionSource != telSourceEnv || rec.Reason == "" {
		t.Errorf("gate-refusal record = %+v", rec)
	}
	// No session id ⇒ no auto-emit (opt-in).
	t.Setenv(telemetryEnvSession, "")
	if code, _, _ := runIn([]string{"transition", "sto:one", "planned"}); code != 1 {
		t.Fatalf("refusal without session: exit = %d, want 1", code)
	}
	if _, err := os.Stat(telemetryPath(dir, "")); !os.IsNotExist(err) {
		t.Errorf("no session id must not auto-emit: stat err = %v", err)
	}
}

// TestTelemetryAutoEmitVerify: verify pass records gate-pass and a verify
// fail records gate-refusal, both gate verify; the read of the
// constraint tree is unaffected.
func TestTelemetryAutoEmitVerify(t *testing.T) {
	units := []*exchange.Unit{agentsMDFixtureUnit("fnd", "product-identity", "Discovery")}
	agentsMDRepo(t, units)
	if code, _, errText := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("agents-md --write: exit = %d\n%s", code, errText)
	}
	t.Setenv(telemetryEnvSession, "sess-v")
	dir, _ := os.Getwd()

	if code, _, errText := runIn([]string{"verify"}); code != 0 {
		t.Fatalf("verify pass: exit = %d\n%s", code, errText)
	}
	path := telemetryPath(dir, "sess-v")
	recs := telemetryRecords(t, path)
	if len(recs) != 1 || recs[0].Kind != telKindGatePass || recs[0].Gate != telGateVerify {
		t.Fatalf("pass record = %+v", recs)
	}

	staleAgentsMD(t, dir)
	if code, _, _ := runIn([]string{"verify"}); code != 1 {
		t.Fatalf("verify fail: exit = %d, want 1", code)
	}
	recs = telemetryRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	fail := recs[1]
	if fail.Kind != telKindGateRefusal || fail.Gate != telGateVerify || !strings.Contains(fail.Reason, "agents-md-drift") {
		t.Errorf("fail record = %+v", fail)
	}
}

// TestTelemetryAutoEmitPublish: two publish successes in a session
// record first-write then write; a conformance-blocked publish records
// gate-refusal (gate conformance); the host output/exit are unchanged.
func TestTelemetryAutoEmitPublish(t *testing.T) {
	w, repo := authoringEnv(t, "feather")
	body := stoBody(t)
	t.Setenv(telemetryEnvSession, "sess-p")

	if code, _, errText := runIn([]string{"new", "feather/sto:one", "--content-file", body}); code != 0 {
		t.Fatalf("new 1: exit = %d\n%s", code, errText)
	}
	if code, out, errText := runIn([]string{"publish", "feather/sto:one"}); code != 0 {
		t.Fatalf("publish 1: exit = %d\n%s\n%s", code, out, errText)
	}
	if code, _, errText := runIn([]string{"new", "feather/sto:two", "--content-file", body}); code != 0 {
		t.Fatalf("new 2: exit = %d\n%s", code, errText)
	}
	if code, out, errText := runIn([]string{"publish", "feather/sto:two"}); code != 0 {
		t.Fatalf("publish 2: exit = %d\n%s\n%s", code, out, errText)
	}
	path := telemetryPath(repo, "sess-p")
	recs := telemetryRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("publish records = %d, want 2: %+v", len(recs), recs)
	}
	if recs[0].Kind != telKindFirstWrite || recs[0].Subject != "feather/sto:one:1" {
		t.Errorf("first record = %+v, want first-write for sto:one:1", recs[0])
	}
	if recs[1].Kind != telKindWrite || recs[1].Subject != "feather/sto:two:1" {
		t.Errorf("second record = %+v, want write for sto:two:1", recs[1])
	}

	// A conformance-blocked publish (a draft that fails CKO validation)
	// records gate-refusal with gate conformance.
	project := projectOf(t, w, mustAbs(t, "."))
	badPath := draftFile(t, w, project, "spec", "broken")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{"namespace":"feather","type":"spec","id":"broken","revision":1,"state":{"contentState":"bogus","existenceState":"active"},"changeLog":[{"date":"2026-08-07","domain":"existenceState","from":"-","to":"active","by":"E"}],"content":{"purpose":"p","content":"c"}}`
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runIn([]string{"publish", "feather/spec:broken"}); code != 1 {
		t.Fatalf("blocked publish: exit = %d, want 1", code)
	}
	recs = telemetryRecords(t, path)
	last := recs[len(recs)-1]
	if last.Kind != telKindGateRefusal || last.Gate != telGateConformance || last.Subject != "feather/spec:broken" {
		t.Errorf("blocked record = %+v", last)
	}
}

// --- Wi-A: reads never write ------------------------------------------

// dirDigest hashes every regular file under dir except the sqlite
// WAL/SHM sidecars, mapping the slash-relative path to its SHA-256.
func dirDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestTelemetryReadsNeverWrite: `eka get` and `eka context` change
// neither the telemetry files nor the workspace store, EVEN with a
// session id present (auto-emit armed) — the exact regression this
// guards is a read path wired to telemetryAutoEmit. Non-vacuity: the
// same armed hook, invoked as a read path would, DOES create telemetry.
func TestTelemetryReadsNeverWrite(t *testing.T) {
	repo := seedGetRepo(t, nil)
	home := os.Getenv("EKA_HOME")
	// Arm auto-emit: with a session id present, any read path that emits
	// would write here. A no-op run (empty env) could not catch that.
	t.Setenv(telemetryEnvSession, "sess-read")

	if _, err := os.Stat(telemetryDirPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("telemetry dir must not exist before the reads: %v", err)
	}
	before := dirDigest(t, home)

	for _, args := range [][]string{
		{"get", "eka-view-fixture/adr:001-login-serialization:1"},
		{"context", "eka-view-fixture/sto:alpha"},
		{"get", "eka-view-fixture/sto:alpha"},
	} {
		if code, out, errText := runIn(args); code != 0 {
			t.Fatalf("%v: exit = %d\nstdout: %s\nstderr: %s", args, code, out, errText)
		}
	}
	// Also with no session env at all (unarmed): still no telemetry.
	t.Setenv(telemetryEnvSession, "")
	if code, _, _ := runIn([]string{"get", "eka-view-fixture/sto:alpha"}); code != 0 {
		t.Fatal("get without a session failed")
	}

	if _, err := os.Stat(telemetryDirPath(repo)); !os.IsNotExist(err) {
		t.Errorf("the read commands wrote telemetry: %v", err)
	}
	if after := dirDigest(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("the read commands changed the store:\nbefore: %v\nafter:  %v", before, after)
	}

	// Non-vacuity: the telemetry-dir guard above flips on exactly the
	// regression it guards — a read path reaching the auto-emit hook with
	// the same armed session id. Drive that hook directly and require the
	// file the assertion forbids to appear.
	t.Setenv(telemetryEnvSession, "sess-read")
	telemetryAutoEmit(telKindConstraintRead, "eka-view-fixture/adr:001-login-serialization:1", "eka-view-fixture/adr:001-login-serialization:1", "", "")
	if _, err := os.Stat(telemetryPath(repo, "sess-read")); err != nil {
		t.Errorf("non-vacuity: a read-path emit must create telemetry, got %v", err)
	}
}

// --- Wi-A: attachment round-trip --------------------------------------

// TestTelemetryAttachmentRoundTrip: docs/telemetry/<id>.jsonl travels as
// a source-only RSF attachment with a repo-relative id and a matching
// digest through exchange.EmitSource / exchange.LoadSnapshot.
func TestTelemetryAttachmentRoundTrip(t *testing.T) {
	dir := telemetryRepo(t)
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s1"}); code != 0 {
		t.Fatal("emit failed")
	}
	data, err := os.ReadFile(telemetryPath(dir, "s1"))
	if err != nil {
		t.Fatal(err)
	}

	pkg := &exchange.Package{
		Header: exchange.Header{
			SerializationVersion:  exchange.SerializationVersion,
			ExchangeFormatVersion: exchange.ExchangeFormatVersion,
			SpecificationVersion:  exchange.SpecificationVersion,
			Exporter:              exchange.Exporter,
			PackageIdentityLabel:  "rsf-repo-demo-1",
			ExportScope:           exchange.ScopeRepository,
			Namespace:             "demo",
		},
		Attachments:  []*exchange.Attachment{{ID: "docs/telemetry/s1.jsonl", Data: data}},
		Declarations: exchange.Declarations{Closure: exchange.ClosureDeclaration{Scope: exchange.ScopeRepository}},
	}
	files, fp, err := exchange.EmitSource(pkg)
	if err != nil {
		t.Fatalf("EmitSource: %v", err)
	}
	wantDigest := pkg.Attachments[0].Digest
	if wantDigest == "" {
		t.Fatal("EmitSource did not fill the attachment digest")
	}

	name := "attachments/docs/telemetry/s1.jsonl"
	found := false
	snapDir := t.TempDir()
	for _, f := range files {
		if f.Name == name {
			found = true
		}
		p := filepath.Join(snapDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatalf("EmitSource did not carry %s: %v", name, files)
	}

	res, err := exchange.LoadSnapshot(snapDir)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if res.Mode != exchange.SnapshotSource {
		t.Errorf("mode = %v, want source-only", res.Mode)
	}
	if res.Digest != fp {
		t.Errorf("digest = %q, want the emitted source fingerprint %q", res.Digest, fp)
	}
	var att *exchange.Attachment
	for _, a := range res.Package.Attachments {
		if a.ID == "docs/telemetry/s1.jsonl" {
			att = a
		}
	}
	if att == nil {
		t.Fatalf("attachment docs/telemetry/s1.jsonl missing: %+v", res.Package.Attachments)
	}
	sum := sha256.Sum256(att.Data)
	if got := hex.EncodeToString(sum[:]); got != wantDigest {
		t.Errorf("loaded attachment digest = %q, want %q", got, wantDigest)
	}
	if !bytes.Equal(att.Data, data) {
		t.Errorf("attachment bytes do not round-trip")
	}
}

// TestTelemetryCaptureSkipList: a telemetry file name never drives
// `eka capture` classification — the docs/ segment it carries would
// otherwise read as a docs signal (non-vacuity below).
func TestTelemetryCaptureSkipList(t *testing.T) {
	if !hasTransportSegment("docs/telemetry/s1.jsonl") {
		t.Fatal("telemetry must be a transport dir")
	}
	kept := excludeTransportPaths([]string{"docs/telemetry/s1.jsonl", "src/x.go"})
	if len(kept) != 1 || kept[0] != "src/x.go" {
		t.Errorf("excludeTransportPaths = %v, want [src/x.go]", kept)
	}
	if sig := classifyPathSignals(excludeTransportPaths([]string{"docs/telemetry/s1.jsonl"})); sig["docs"] {
		t.Errorf("a telemetry file must not signal docs after exclusion: %v", sig)
	}
	// Non-vacuity: without the skip-list the same path IS a docs signal.
	if sig := classifyPathSignals([]string{"docs/telemetry/s1.jsonl"}); !sig["docs"] {
		t.Errorf("non-vacuity: the raw telemetry path must read as a docs signal")
	}
}

// --- Wi-B: list -------------------------------------------------------

// TestTelemetryListJSONStableAndOrdered: --json is byte-identical across
// two runs and records are sorted by (sessionId, seq).
func TestTelemetryListJSONStableAndOrdered(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "bbb", []telemetryRecord{{Kind: "write"}, {Kind: "write"}})
	writeTelemetryFile(t, dir, "aaa", []telemetryRecord{{Kind: "write"}})

	code, out1, errText := runIn([]string{"telemetry", "list", "--json"})
	if code != 0 {
		t.Fatalf("list --json: exit = %d\n%s", code, errText)
	}
	_, out2, _ := runIn([]string{"telemetry", "list", "--json"})
	if out1 != out2 {
		t.Errorf("list --json is not byte-stable:\n%q\n%q", out1, out2)
	}
	recs := listJSONLines(t)
	got := make([]string, 0, len(recs))
	for _, r := range recs {
		got = append(got, r.SessionID+"/"+strconv.Itoa(r.Seq))
	}
	want := []string{"aaa/0", "bbb/0", "bbb/1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestTelemetryListFilters: --session, --repo, --since and --until filter
// deterministically; a malformed --since is a usage error.
func TestTelemetryListFilters(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "s1", []telemetryRecord{
		{Repo: "demo/app", Kind: "write", At: "2026-11-01T00:00:00Z"},
		{Repo: "demo/app", Kind: "gate-pass", Gate: "verify", At: "2026-11-22T00:00:00Z"},
	})
	writeTelemetryFile(t, dir, "s2", []telemetryRecord{
		{Repo: "other/repo", Kind: "write", At: "2026-11-22T00:00:00Z"},
	})

	if got := listJSONLines(t, "--session", "s2"); len(got) != 1 || got[0].SessionID != "s2" {
		t.Errorf("--session s2 = %+v", got)
	}
	if got := listJSONLines(t, "--repo", "demo/app"); len(got) != 2 {
		t.Errorf("--repo demo/app = %+v, want 2", got)
	}
	if got := listJSONLines(t, "--since", "2026-11-10T00:00:00Z"); len(got) != 2 {
		t.Errorf("--since = %+v, want 2", got)
	}
	if got := listJSONLines(t, "--until", "2026-11-10T00:00:00Z"); len(got) != 1 || got[0].SessionID != "s1" || got[0].Seq != 0 {
		t.Errorf("--until = %+v, want the single 2026-11-01 record", got)
	}
	if got := listJSONLines(t, "--since", "2026-11-10T00:00:00Z", "--until", "2026-11-23T00:00:00Z", "--repo", "demo/app"); len(got) != 1 || got[0].Seq != 1 {
		t.Errorf("combined filter = %+v", got)
	}
	if code, _, _ := runIn([]string{"telemetry", "list", "--since", "not-a-time"}); code != 2 {
		t.Errorf("bad --since: exit = %d, want 2", code)
	}
}

// TestTelemetryListMalformed: a malformed line is a deterministic error
// naming the file and line, never silently skipped; without it the same
// file lists cleanly (non-vacuity).
func TestTelemetryListMalformed(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "bad", []telemetryRecord{{Repo: "demo/app", Kind: "write", At: "2026-11-22T00:00:00Z"}})
	path := telemetryPath(dir, "bad")

	if code, _, errText := runIn([]string{"telemetry", "list"}); code != 0 {
		t.Fatalf("clean list: exit = %d, want 0\n%s", code, errText)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{ this is not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	code, _, errText := runIn([]string{"telemetry", "list"})
	if code != 1 {
		t.Fatalf("malformed list: exit = %d, want 1\n%s", code, errText)
	}
	if !strings.Contains(errText, path+":2") || !strings.Contains(errText, "malformed record") {
		t.Errorf("stderr must name the file:line and the malformed record, got %q", errText)
	}
}

// --- M5: report -------------------------------------------------------

// squashSpaces collapses whitespace runs to single spaces, so a human-
// output assertion is robust to the report's column alignment.
func squashSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// telemetryReportJSON runs `eka telemetry report --json` and decodes it.
func telemetryReportJSON(t *testing.T, args ...string) telemetryReport {
	t.Helper()
	code, out, errText := runIn(append([]string{"telemetry", "report", "--json"}, args...))
	if code != 0 {
		t.Fatalf("report %v: exit = %d\nstderr: %s", args, code, errText)
	}
	var rep telemetryReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	return rep
}

// writeTelemetryReportFixture writes the M5 acceptance fixture: one
// compliant session (constraint-read then first-write) and one
// non-compliant session (a write with no prior read, then a gate-refusal
// on demo/sto:beta with no repair).
func writeTelemetryReportFixture(t *testing.T, dir string) {
	t.Helper()
	writeTelemetryFile(t, dir, "comp", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindConstraintRead, Subject: "demo/adr:001-x:1", Constraint: "demo/adr:001-x:1", At: "2026-11-20T00:00:00Z", SessionSource: telSourceClient},
		{Repo: "demo/app", Kind: telKindFirstWrite, Subject: "demo/sto:alpha:1", At: "2026-11-20T00:00:01Z", SessionSource: telSourceClient},
	})
	writeTelemetryFile(t, dir, "noncomp", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindWrite, Subject: "demo/sto:beta:1", At: "2026-11-21T00:00:00Z", SessionSource: telSourceClient},
		{Repo: "demo/app", Kind: telKindGateRefusal, Gate: telGateTransition, Subject: "demo/sto:beta", Reason: "transition refused", At: "2026-11-21T00:00:01Z", SessionSource: telSourceClient},
	})
}

// TestTelemetryReportAggregates: the four M5 numbers and the limits for
// the compliant+non-compliant fixture, plus the human render. Non-
// vacuity: removing a session flips sessions observed to 1.
func TestTelemetryReportAggregates(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)

	rep := telemetryReportJSON(t)
	if rep.SessionsObserved != 2 || rep.ReadBeforeFirstWrite != 1 || rep.GateFailures != 1 || rep.RepairedInSession != 0 {
		t.Errorf("numbers = observed %d, read-before %d, gate %d, repaired %d; want 2, 1, 1, 0",
			rep.SessionsObserved, rep.ReadBeforeFirstWrite, rep.GateFailures, rep.RepairedInSession)
	}
	lim := rep.Limits
	if lim.UnattributedSessions != 0 || lim.SessionsWithoutCapture != "not-observable" ||
		lim.TelemetryStart != "2026-11-20T00:00:00Z" ||
		!lim.ReadsEmitted || lim.ReadBeforeFirstWriteIsFloor || !lim.RepairedInSessionIsUpperBound {
		t.Errorf("limits = %+v", lim)
	}

	code, out, errText := runIn([]string{"telemetry", "report"})
	if code != 0 {
		t.Fatalf("human report: exit = %d\n%s", code, errText)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("non-TTY report must carry no ANSI:\n%q", out)
	}
	for _, want := range []string{
		"Telemetry", "Report",
		"sessions observed: 2",
		"read a constraint before first write: 1",
		"gate failures: 1",
		"repaired in same session: 0",
		"unattributed sessions (excluded): 0",
		"sessions without capture: not observable (only sessions that emitted telemetry are counted)",
		"telemetry start: 2026-11-20T00:00:00Z",
		"reads emitted: yes",
		"repaired-in-session is an upper bound",
	} {
		if !strings.Contains(squashSpaces(out), want) {
			t.Errorf("human report missing %q:\n%s", want, out)
		}
	}

	// Non-vacuity: dropping a session flips sessions observed to 1.
	if err := os.Remove(telemetryPath(dir, "comp")); err != nil {
		t.Fatal(err)
	}
	if got := telemetryReportJSON(t).SessionsObserved; got != 1 {
		t.Errorf("non-vacuity sessions observed = %d, want 1 after removing a session", got)
	}
}

// TestTelemetryReportReadBeforeAndFloor: removing the only constraint-read
// flips read-before-first-write to 0 and, with no read left in scope,
// marks the number a floor (">="). Non-vacuity: the base fixture has a
// read, so the number is exact.
func TestTelemetryReportReadBeforeAndFloor(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)
	if rep := telemetryReportJSON(t); rep.ReadBeforeFirstWrite != 1 || rep.Limits.ReadBeforeFirstWriteIsFloor {
		t.Fatalf("base read-before = %d floor %v; want 1 exact", rep.ReadBeforeFirstWrite, rep.Limits.ReadBeforeFirstWriteIsFloor)
	}
	writeTelemetryFile(t, dir, "comp", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindFirstWrite, At: "2026-11-20T00:00:01Z", SessionSource: telSourceClient},
	})
	rep := telemetryReportJSON(t)
	if rep.ReadBeforeFirstWrite != 0 || !rep.Limits.ReadBeforeFirstWriteIsFloor || rep.Limits.ReadsEmitted {
		t.Errorf("after removing the read: read-before = %d, readsEmitted = %v, floor = %v; want 0, false, true",
			rep.ReadBeforeFirstWrite, rep.Limits.ReadsEmitted, rep.Limits.ReadBeforeFirstWriteIsFloor)
	}
	code, out, _ := runIn([]string{"telemetry", "report"})
	if code != 0 || !strings.Contains(squashSpaces(out), "read a constraint before first write: >= 0") {
		t.Errorf("human floor label missing (exit %d):\n%s", code, out)
	}
	// The limits section states the read coverage honestly (no read emitted).
	for _, want := range []string{
		"reads emitted: no",
		"read-before-first-write is a floor (>=): no read was emitted in scope, so the number is an undercount",
		"sessions without capture: not observable (only sessions that emitted telemetry are counted)",
	} {
		if !strings.Contains(squashSpaces(out), want) {
			t.Errorf("no-read human report missing %q:\n%s", want, out)
		}
	}
}

// TestTelemetryReportGateFailures: the gate-refusal count flips to 0 when
// its only input is removed. Non-vacuity: the base count of 1 is asserted
// first.
func TestTelemetryReportGateFailures(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)
	if got := telemetryReportJSON(t).GateFailures; got != 1 {
		t.Fatalf("base gate failures = %d, want 1", got)
	}
	writeTelemetryFile(t, dir, "noncomp", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindWrite, At: "2026-11-21T00:00:00Z", SessionSource: telSourceClient},
	})
	if got := telemetryReportJSON(t).GateFailures; got != 0 {
		t.Errorf("gate failures = %d, want 0 after removing the refusal", got)
	}
}

// TestTelemetryReportRepair: a gate-refusal followed (higher seq, same
// session) by a gate-pass on the same subject counts as repaired. Non-
// vacuity: a different subject and a pass before the refusal do not.
func TestTelemetryReportRepair(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "rep", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindGateRefusal, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:00Z", SessionSource: telSourceClient},
		{Repo: "demo/app", Kind: telKindGatePass, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:01Z", SessionSource: telSourceClient},
	})
	if got := telemetryReportJSON(t).RepairedInSession; got != 1 {
		t.Errorf("repaired = %d, want 1 (refusal then a later pass, same subject)", got)
	}
	// A pass on a different subject does not repair the refusal.
	writeTelemetryFile(t, dir, "rep", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindGateRefusal, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:00Z", SessionSource: telSourceClient},
		{Repo: "demo/app", Kind: telKindGatePass, Gate: telGateTransition, Subject: "demo/sto:other", At: "2026-11-22T00:00:01Z", SessionSource: telSourceClient},
	})
	if got := telemetryReportJSON(t).RepairedInSession; got != 0 {
		t.Errorf("repaired = %d, want 0 (the pass is on a different subject)", got)
	}
	// The pass BEFORE the refusal does not repair it.
	writeTelemetryFile(t, dir, "rep", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindGatePass, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:00Z", SessionSource: telSourceClient},
		{Repo: "demo/app", Kind: telKindGateRefusal, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:01Z", SessionSource: telSourceClient},
	})
	if got := telemetryReportJSON(t).RepairedInSession; got != 0 {
		t.Errorf("repaired = %d, want 0 (the pass precedes the refusal)", got)
	}
}

// TestTelemetryReportUnattributed: an unattributed session file (.jsonl,
// sessionId "") increments the unattributed limit and not sessions
// observed. Non-vacuity: the base fixture has no unattributed session.
func TestTelemetryReportUnattributed(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)
	if rep := telemetryReportJSON(t); rep.Limits.UnattributedSessions != 0 || rep.SessionsObserved != 2 {
		t.Fatalf("base = unattributed %d, observed %d; want 0, 2", rep.Limits.UnattributedSessions, rep.SessionsObserved)
	}
	writeTelemetryFile(t, dir, "", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindWrite, At: "2026-11-23T00:00:00Z", SessionSource: telSourceUnattributed},
	})
	rep := telemetryReportJSON(t)
	if rep.Limits.UnattributedSessions != 1 {
		t.Errorf("unattributed sessions = %d, want 1", rep.Limits.UnattributedSessions)
	}
	if rep.SessionsObserved != 2 {
		t.Errorf("sessions observed = %d, want 2 (the unattributed session is excluded)", rep.SessionsObserved)
	}
}

// TestTelemetryReportRepairExcludesUnattributed: a refusal-then-pass pair
// in the unattributed bucket (sessionId "") is NOT a per-session repair —
// the bucket pools unrelated runs. The identical pair inside an attributed
// session IS repaired (non-vacuity: the assertion flips on attribution,
// not on the pair).
func TestTelemetryReportRepairExcludesUnattributed(t *testing.T) {
	dir := telemetryRepo(t)
	pair := func(src string) []telemetryRecord {
		return []telemetryRecord{
			{Repo: "demo/app", Kind: telKindGateRefusal, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:00Z", SessionSource: src},
			{Repo: "demo/app", Kind: telKindGatePass, Gate: telGateTransition, Subject: "demo/sto:g", At: "2026-11-22T00:00:01Z", SessionSource: src},
		}
	}

	// Unattributed: no session observed, no repair, the refusal still
	// counts toward the cross-scope gate-failure total.
	writeTelemetryFile(t, dir, "", pair(telSourceUnattributed))
	rep := telemetryReportJSON(t)
	if rep.RepairedInSession != 0 {
		t.Errorf("unattributed repaired = %d, want 0 (the bucket is not a session)", rep.RepairedInSession)
	}
	if rep.SessionsObserved != 0 || rep.GateFailures != 1 || rep.Limits.UnattributedSessions != 1 {
		t.Errorf("unattributed report = observed %d, gate %d, unattributed %d; want 0, 1, 1",
			rep.SessionsObserved, rep.GateFailures, rep.Limits.UnattributedSessions)
	}

	// Non-vacuity: the identical pair in an attributed session IS repaired.
	if err := os.Remove(telemetryPath(dir, "")); err != nil {
		t.Fatal(err)
	}
	writeTelemetryFile(t, dir, "s", pair(telSourceClient))
	rep = telemetryReportJSON(t)
	if rep.RepairedInSession != 1 {
		t.Errorf("attributed repaired = %d, want 1 (the same pair, attributed)", rep.RepairedInSession)
	}
	if rep.SessionsObserved != 1 || rep.GateFailures != 1 || rep.Limits.UnattributedSessions != 0 {
		t.Errorf("attributed report = observed %d, gate %d, unattributed %d; want 1, 1, 0",
			rep.SessionsObserved, rep.GateFailures, rep.Limits.UnattributedSessions)
	}
}

// TestTelemetryReportFilters: --repo, --since and --until narrow the scope
// exactly like `list` (inclusive on `at`); a malformed timestamp is a
// usage error.
func TestTelemetryReportFilters(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)

	if rep := telemetryReportJSON(t, "--repo", "demo/app"); rep.SessionsObserved != 2 {
		t.Errorf("--repo demo/app observed = %d, want 2", rep.SessionsObserved)
	}
	if rep := telemetryReportJSON(t, "--repo", "other/x"); rep.SessionsObserved != 0 || rep.Limits.TelemetryStart != "" {
		t.Errorf("--repo other/x = %+v, want an empty scope", rep)
	}
	// --since excludes the 2026-11-20 compliant session from the numbers,
	// but the telemetry start stays at the period-independent global min.
	since := telemetryReportJSON(t, "--since", "2026-11-21T00:00:00Z")
	if since.SessionsObserved != 1 || since.ReadBeforeFirstWrite != 0 || since.GateFailures != 1 ||
		since.Limits.TelemetryStart != "2026-11-20T00:00:00Z" || since.Limits.ReadsEmitted {
		t.Errorf("--since = %+v", since)
	}
	// --until keeps only the compliant session.
	until := telemetryReportJSON(t, "--until", "2026-11-20T12:00:00Z")
	if until.SessionsObserved != 1 || until.ReadBeforeFirstWrite != 1 || until.GateFailures != 0 ||
		until.Limits.TelemetryStart != "2026-11-20T00:00:00Z" {
		t.Errorf("--until = %+v", until)
	}
	if code, _, _ := runIn([]string{"telemetry", "report", "--since", "not-a-time"}); code != 2 {
		t.Errorf("bad --since: exit = %d, want 2", code)
	}
}

// TestTelemetryReportJSONStable: --json is byte-identical across two runs.
func TestTelemetryReportJSONStable(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)
	code, out1, errText := runIn([]string{"telemetry", "report", "--json"})
	if code != 0 {
		t.Fatalf("report --json: exit = %d\n%s", code, errText)
	}
	_, out2, _ := runIn([]string{"telemetry", "report", "--json"})
	if out1 != out2 {
		t.Errorf("report --json is not byte-stable:\n%q\n%q", out1, out2)
	}
}

// TestTelemetryReportEmpty: with no telemetry directory the scope is empty
// — exit 0, zero sessions and an empty telemetry start (never a bare 0).
func TestTelemetryReportEmpty(t *testing.T) {
	telemetryRepo(t)
	rep := telemetryReportJSON(t)
	if rep.SessionsObserved != 0 || rep.ReadBeforeFirstWrite != 0 || rep.GateFailures != 0 || rep.RepairedInSession != 0 {
		t.Errorf("empty numbers = %+v, want all zero", rep)
	}
	if rep.Limits.TelemetryStart != "" || rep.Limits.ReadsEmitted || !rep.Limits.ReadBeforeFirstWriteIsFloor ||
		rep.Limits.SessionsWithoutCapture != "not-observable" {
		t.Errorf("empty limits = %+v", rep.Limits)
	}
	code, out, errText := runIn([]string{"telemetry", "report"})
	if code != 0 || errText != "" {
		t.Fatalf("empty human: exit = %d, stderr = %q", code, errText)
	}
	if strings.Contains(out, "\x1b") ||
		!strings.Contains(squashSpaces(out), "telemetry start: (none)") ||
		!strings.Contains(squashSpaces(out), "sessions without capture: not observable (only sessions that emitted telemetry are counted)") {
		t.Errorf("empty human report = %q", out)
	}
}

// TestTelemetryReportStartPeriodIndependent: telemetry scope is the
// repository stream start, not the period minimum — a --since later than
// the stream's earliest record narrows the numbers but leaves the start at
// the global minimum, and --repo still scopes it. Non-vacuity: with no
// --since the start is the same value.
func TestTelemetryReportStartPeriodIndependent(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir) // earliest record @ 2026-11-20

	base := telemetryReportJSON(t)
	if base.Limits.TelemetryStart != "2026-11-20T00:00:00Z" || base.SessionsObserved != 2 {
		t.Fatalf("base = start %q observed %d; want 2026-11-20T00:00:00Z, 2", base.Limits.TelemetryStart, base.SessionsObserved)
	}
	since := telemetryReportJSON(t, "--since", "2026-11-21T00:00:00Z")
	if since.Limits.TelemetryStart != "2026-11-20T00:00:00Z" {
		t.Errorf("telemetry start under --since = %q, want the period-independent 2026-11-20T00:00:00Z", since.Limits.TelemetryStart)
	}
	if since.SessionsObserved != 1 || since.GateFailures != 1 || since.ReadBeforeFirstWrite != 0 {
		t.Errorf("--since must still narrow the aggregates: %+v", since)
	}

	// --repo scopes the start: an earlier record in another repo does not
	// lower the demo/app stream start.
	writeTelemetryFile(t, dir, "other", []telemetryRecord{
		{Repo: "other/x", Kind: telKindWrite, At: "2020-01-01T00:00:00Z", SessionSource: telSourceClient},
	})
	if got := telemetryReportJSON(t, "--repo", "demo/app").Limits.TelemetryStart; got != "2026-11-20T00:00:00Z" {
		t.Errorf("--repo demo/app start = %q, want 2026-11-20T00:00:00Z (other repo ignored)", got)
	}
}

// TestTelemetryReportMalformed: a malformed line is a deterministic error
// naming the file and line (exit 1), never silently skipped.
func TestTelemetryReportMalformed(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryReportFixture(t, dir)
	// Non-vacuity: the clean fixture exits 0, so the exit-1 below is not
	// trivially always-fail.
	if code, _, errText := runIn([]string{"telemetry", "report"}); code != 0 {
		t.Fatalf("clean report: exit = %d, want 0\n%s", code, errText)
	}
	path := telemetryPath(dir, "noncomp")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{ not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	code, _, errText := runIn([]string{"telemetry", "report"})
	if code != 1 {
		t.Fatalf("malformed report: exit = %d, want 1\n%s", code, errText)
	}
	if !strings.Contains(errText, path+":3") || !strings.Contains(errText, "malformed record") {
		t.Errorf("stderr must name the file:line and the malformed record, got %q", errText)
	}
}

// TestTelemetryReportInvalidAt: a record whose `at` is not RFC3339 is a
// deterministic error (the report needs `at` for the telemetry start).
func TestTelemetryReportInvalidAt(t *testing.T) {
	dir := telemetryRepo(t)
	writeTelemetryFile(t, dir, "s1", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindWrite, At: "not-a-time", SessionSource: telSourceClient},
	})
	code, _, errText := runIn([]string{"telemetry", "report"})
	if code != 1 {
		t.Fatalf("invalid at: exit = %d, want 1\n%s", code, errText)
	}
	if !strings.Contains(errText, "invalid at timestamp") {
		t.Errorf("stderr must name the invalid at, got %q", errText)
	}

	// Non-vacuity: the same record with a valid `at` exits 0.
	writeTelemetryFile(t, dir, "s1", []telemetryRecord{
		{Repo: "demo/app", Kind: telKindWrite, At: "2026-11-22T00:00:00Z", SessionSource: telSourceClient},
	})
	if code, _, errText := runIn([]string{"telemetry", "report"}); code != 0 {
		t.Fatalf("valid at: exit = %d, want 0\n%s", code, errText)
	}
}

// TestTelemetryListHumanAndEmpty: the human render follows the ui
// conventions (plain when piped) and an absent telemetry directory is an
// empty result.
func TestTelemetryListHumanAndEmpty(t *testing.T) {
	dir := telemetryRepo(t)
	// No telemetry dir yet: empty result, exit 0, no ANSI.
	code, out, errText := runIn([]string{"telemetry", "list"})
	if code != 0 || errText != "" {
		t.Fatalf("empty list: exit = %d, stderr = %q", code, errText)
	}
	if strings.Contains(out, "\x1b") || !strings.Contains(out, "(no records)") {
		t.Errorf("empty human list = %q", out)
	}

	writeTelemetryFile(t, dir, "s1", []telemetryRecord{{Repo: "demo/app", Kind: "gate-refusal", Gate: "verify", Subject: "x", Reason: "boom", At: "2026-11-22T00:00:00Z"}})
	code, out, _ = runIn([]string{"telemetry", "list"})
	if code != 0 {
		t.Fatalf("human list: exit = %d", code)
	}
	for _, want := range []string{"Telemetry", "Records:", "s1 #0 gate-refusal", "gate=verify", "reason=boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("human list missing %q:\n%s", want, out)
		}
	}
}
