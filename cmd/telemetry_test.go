package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/exchange"
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
// neither the telemetry files nor the workspace store. Non-vacuity: an
// actual telemetry write appears in the same detector.
func TestTelemetryReadsNeverWrite(t *testing.T) {
	repo := seedGetRepo(t, nil)
	home := os.Getenv("EKA_HOME")
	t.Setenv(telemetryEnvSession, "")

	if _, err := os.Stat(telemetryDirPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("telemetry dir must not exist before the reads: %v", err)
	}
	before := dirDigest(t, home)

	for _, args := range [][]string{
		{"get", "eka-view-fixture/adr:001-login-serialization:1"},
		{"context", "eka-view-fixture/sto:alpha"},
	} {
		if code, out, errText := runIn(args); code != 0 {
			t.Fatalf("%v: exit = %d\nstdout: %s\nstderr: %s", args, code, out, errText)
		}
	}

	if _, err := os.Stat(telemetryDirPath(repo)); !os.IsNotExist(err) {
		t.Errorf("the read commands wrote telemetry: %v", err)
	}
	if after := dirDigest(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("the read commands changed the store:\nbefore: %v\nafter:  %v", before, after)
	}

	// Non-vacuity: an actual write IS visible to the telemetry-file check.
	if code, _, _ := runIn([]string{"telemetry", "emit", "--kind", "write", "--session", "s"}); code != 0 {
		t.Fatal("emit failed")
	}
	if _, err := os.Stat(telemetryDirPath(repo)); err != nil {
		t.Errorf("a write must create the telemetry dir (detector non-vacuous): %v", err)
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
