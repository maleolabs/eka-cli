package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the note enhancements (ADR-019 D8 revised):
//   - `eka note` accepts EVERY canonical artifact type as the subject
//     (not just work items);
//   - `eka note --domain` declares the contextable Engineering Domain
//     (one of the five canonical domains), stored canonically and
//     honored at publish;
//   - `eka view ticket --with-note/--with-comments` surfaces the notes
//     discussing the ticket and its work item;
//   - `eka get ticket <target> --with-notes/--with-comments` emits the
//     machine ticket document (schema eka-ticket-v1).

// artifactState returns a minimal valid state + change-log pair for an
// artifact type: every owned domain gets its initial transition, the
// change log in occurrence order.
func artifactState(t *testing.T, typeToken string) (map[string]string, []map[string]string) {
	t.Helper()
	var states map[string]string
	var log []map[string]string
	switch typeToken {
	case "sto", "ts", "bug", "td", "ch", "spk":
		states = map[string]string{"executionState": "todo", "existenceState": "active"}
		log = []map[string]string{
			{"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Test"},
			{"date": "2026-08-10", "domain": "executionState", "from": "-", "to": "planned", "by": "Test"},
			{"date": "2026-08-10", "domain": "executionState", "from": "planned", "to": "todo", "by": "Test"},
		}
	case "ctr":
		states = map[string]string{"containerState": "active", "existenceState": "active"}
		log = []map[string]string{
			{"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Test"},
			{"date": "2026-08-10", "domain": "containerState", "from": "-", "to": "active", "by": "Test"},
		}
	case "tkt":
		states = map[string]string{}
		log = []map[string]string{}
	default: // content-state types (adr, epc, run, vis, ...)
		content := "draft"
		if typeToken == "adr" || typeToken == "dec" {
			content = "accepted" // decision content states: proposed, accepted, superseded
		}
		states = map[string]string{"contentState": content, "existenceState": "active"}
		log = []map[string]string{
			{"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Test"},
			{"date": "2026-08-10", "domain": "contentState", "from": "-", "to": content, "by": "Test"},
		}
	}
	return states, log
}

// artifactDimension returns the required primary dimension of a
// knowledge artifact type (R6), or "" for types without one.
func artifactDimension(typeToken string) string {
	switch typeToken {
	case "vis", "str":
		return "intent"
	case "req":
		return "requirements"
	case "fnd":
		return "research"
	case "adr", "dec":
		return "decisions"
	case "arc":
		return "architecture"
	case "spec":
		return "specifications"
	case "std":
		return "standards"
	case "gls":
		return "vocabulary"
	case "scp", "epc", "plan", "trc":
		return "planning"
	case "run":
		return "operations"
	case "rel":
		return "records"
	default:
		return ""
	}
}

// artifactContent returns a minimal valid content object per type.
func artifactContent(typeToken string) map[string]any {
	switch typeToken {
	case "sto", "ts", "ch":
		return map[string]any{"description": "A story.", "acceptanceCriteria": "- Works."}
	case "bug":
		return map[string]any{"description": "A bug.", "impact": "Breaks things."}
	case "td":
		return map[string]any{"description": "Debt.", "acceptanceCriteria": "- Paid.", "debtRationale": "Speed."}
	case "spk":
		return map[string]any{"description": "A spike.", "investigationNotes": "- Look.", "conclusion": "Do it."}
	case "adr", "dec":
		return map[string]any{"context": "C.", "decision": "D.", "consequences": "- Good.", "alternativesConsidered": "- None."}
	case "vis", "str", "req", "arc", "spec", "std", "run", "rel", "gls":
		return map[string]any{"purpose": "P.", "content": "Body."}
	case "fnd":
		return map[string]any{"purpose": "P.", "content": "Body.", "investigationSummary": "S.", "conclusion": "C."}
	case "scp", "epc", "plan", "trc":
		return map[string]any{"objective": "O.", "scope": "S.", "outOfScope": "No."}
	case "ctr":
		return map[string]any{"objective": "O.", "workItems": "- x", "changeLog": "- y"}
	case "tkt":
		return map[string]any{"commands": "> Generated — State Projection. Do NOT edit state here; refresh on read.\n", "projectedStatus": ""}
	default:
		return map[string]any{}
	}
}

// artifactPath returns the convention location of an artifact type.
func artifactPath(repo, typeToken, id string) string {
	dir := map[string]string{
		"sto": "operating/work-items/stories", "ts": "operating/work-items/technical-stories",
		"bug": "operating/work-items/bugs", "td": "operating/work-items/tech-debt",
		"ch": "operating/work-items/chores", "spk": "operating/work-items/spikes",
		"ctr": "operating/containers", "tkt": "operating/projections",
		"cmt": "operating/notes", "ses": "operating/sessions",
		"adr": "decisions", "dec": "decisions",
		"vis": "intent", "str": "intent", "req": "requirements", "fnd": "research",
		"arc": "architecture", "spec": "specifications", "std": "standards", "gls": "vocabulary",
		"scp": "planning", "epc": "planning", "plan": "planning", "trc": "planning",
		"run": "operations", "rel": "records", "rvw": "quality",
	}[typeToken]
	return filepath.Join(repo, "docs", dir, typeToken+"-"+id+".json")
}

// writeArtifact writes one valid authoring artifact to its convention
// location. extra maps to the relationships field verbatim.
func writeArtifact(t *testing.T, repo, typeToken, id string, extra map[string][]string) {
	t.Helper()
	states, log := artifactState(t, typeToken)
	doc := map[string]any{
		"namespace": "test-ns", "type": typeToken, "id": id,
		"instanceVersion": 1, "revision": 1,
		"author": "Test", "created": "2026-08-10", "updated": "2026-08-10",
		"state":     states,
		"changeLog": log,
		"content":   artifactContent(typeToken),
	}
	if dim := artifactDimension(typeToken); dim != "" {
		doc["dimension"] = dim
	}
	if len(extra) > 0 {
		doc["relationships"] = extra
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := artifactPath(repo, typeToken, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// noteEnvMulti builds a registered repository whose store carries one
// artifact per canonical-type group: sto (execution), bug (execution),
// adr (architecture), epc (planning), run (operations), vis
// (discovery), ctr (container) and tkt (ticket deriving from sto + ctr).
func noteEnvMulti(t *testing.T) string {
	t.Helper()
	gitIdentityEnv(t, "test-agent")
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	repo := t.TempDir()
	writeEkaYAML(t, repo, "proj", "repo", "test-ns")
	m := metadata.Metadata{Version: 1, Project: "proj", Name: "repo", Namespace: "test-ns"}
	if _, _, _, err := w.RegisterRepoMetadata(repo, m); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		typeToken, id string
		rels          map[string][]string
	}{
		{"sto", "one", nil},
		{"bug", "two", nil},
		{"adr", "three", nil},
		{"epc", "four", nil},
		{"run", "five", nil},
		{"vis", "six", nil},
		{"ctr", "w1", nil},
		{"tkt", "t-one", map[string][]string{"derivesFrom": {"ctr:w1", "sto:one"}}},
	} {
		writeArtifact(t, repo, tt.typeToken, tt.id, tt.rels)
	}
	t.Chdir(repo)
	if code, _, errText := runIn([]string{"sync"}); code != 0 {
		t.Fatalf("seed sync: exit = %d, stderr %q", code, errText)
	}
	return repo
}

// TestNoteDraftAllCanonicalTypes: `eka note` accepts ANY canonical
// artifact type as the subject — the draft is created with the
// discusses wiring to the resolved subject line, and the repository
// docs tree stays untouched.
func TestNoteDraftAllCanonicalTypes(t *testing.T) {
	repo := noteEnvMulti(t)
	body := noteBody(t, `{"verdict": "approve", "notes": ["LGTM"]}`)
	for _, tt := range []struct{ typeToken, id string }{
		{"sto", "one"}, {"bug", "two"}, {"adr", "three"},
		{"epc", "four"}, {"run", "five"}, {"vis", "six"},
		{"ctr", "w1"}, {"tkt", "t-one"},
	} {
		target := tt.typeToken + ":" + tt.id
		code, out, errText := runIn([]string{"note", target, "--role", "review", "--content-file", body, "--by", "agent-x"})
		if code != 0 {
			t.Fatalf("note %s: exit = %d, stderr %q, stdout %q", target, code, errText, out)
		}
		draftPath := draftPathOf(t, tt.id+"-review")
		raw, err := os.ReadFile(draftPath)
		if err != nil {
			t.Fatalf("note %s: draft not found at %s: %v", target, draftPath, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("note %s: draft is not valid JSON: %v", target, err)
		}
		discusses := doc["relationships"].(map[string]any)["discusses"].([]any)
		want := "test-ns/" + target
		if discusses[0] != want {
			t.Errorf("note %s: draft discusses = %v, want %s", target, discusses, want)
		}
	}
	// The docs tree gained no cmt files (drafts live in EKA_HOME).
	docs, err := filepath.Glob(filepath.Join(repo, "docs", "**", "cmt-*"))
	if err != nil || len(docs) != 0 {
		t.Errorf("no cmt file may be written to the repo docs tree, got %v", docs)
	}
}

// TestNoteDomainContextable: --domain declares the note's Engineering
// Domain — any of the five canonical domains (name or lowercase token,
// case-insensitive) — stored canonically in the draft and honored at
// publish; unknown domains are refused (exit 2).
func TestNoteDomainContextable(t *testing.T) {
	noteEnvMulti(t)
	body := noteBody(t, `{"summary": "done", "changes": ["x"], "tests": ["y"]}`)

	for _, tt := range []struct{ flag, want string }{
		{"architecture", "Architecture"},
		{"Architecture", "Architecture"},
		{"PLANNING", "Planning"},
		{"operations", "Operations"},
		{"discovery", "Discovery"},
		{"execution", "Execution"},
	} {
		code, _, errText := runIn([]string{"note", "sto:one", "--role", "implementation",
			"--domain", tt.flag, "--content-file", body, "--by", "agent-x"})
		if code != 0 {
			t.Fatalf("note --domain %s: exit = %d, stderr %q", tt.flag, code, errText)
		}
		// The deterministic id ("one-implementation") gets a numeric
		// suffix once the base is taken by a published unit — resolve
		// the just-created draft from the drafts directory.
		drafts, err := filepath.Glob(filepath.Join(os.Getenv("EKA_HOME"), "drafts", "proj", "cmt-one-implementation*.json"))
		if err != nil || len(drafts) == 0 {
			t.Fatalf("no draft found after note --domain %s", tt.flag)
		}
		latest := drafts[len(drafts)-1]
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(latest), "cmt-"), ".json")
		raw, err := os.ReadFile(latest)
		if err != nil {
			t.Fatalf("draft not found: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc["domain"] != tt.want {
			t.Errorf("note --domain %s: draft domain = %v, want %q", tt.flag, doc["domain"], tt.want)
		}
		// Publish and verify the unit carries the declared domain.
		if code, _, _ := runIn([]string{"publish", "cmt:" + id}); code != 0 {
			t.Fatalf("publish cmt:%s failed", id)
		}
		code, out, _ := runIn([]string{"get", "test-ns/cmt:" + id, "--compact"})
		if code != 0 {
			t.Fatalf("get cmt:%s: exit = %d", id, code)
		}
		var unit struct {
			Classification struct {
				Domain string `json:"domain"`
			} `json:"classification"`
		}
		if err := json.Unmarshal([]byte(out), &unit); err != nil {
			t.Fatal(err)
		}
		if unit.Classification.Domain != tt.want {
			t.Errorf("note --domain %s: published classification.domain = %q, want %q", tt.flag, unit.Classification.Domain, tt.want)
		}
	}
}

// TestNoteDomainRefused: an unknown --domain is a usage error (exit 2)
// with the five canonical domains listed.
func TestNoteDomainRefused(t *testing.T) {
	noteEnvMulti(t)
	body := noteBody(t, `{"summary": "s", "changes": [], "tests": []}`)
	code, _, errText := runIn([]string{"note", "sto:one", "--role", "implementation",
		"--domain", "bogus", "--content-file", body, "--by", "agent-x"})
	if code != 2 {
		t.Fatalf("note --domain bogus: exit = %d, want 2 (usage); stderr %q", code, errText)
	}
	if !strings.Contains(errText, "unknown engineering domain") || !strings.Contains(errText, "Architecture") {
		t.Errorf("stderr = %q, want the unknown-domain refusal listing the canonical domains", errText)
	}
}

// seedCmtNote writes one valid cmt- note into the repo docs tree and
// syncs it into the store (the legacy authoring path keeps working; the
// draft path is exercised by the note tests above).
func seedCmtNote(t *testing.T, repo, id, discusses, verdict string) {
	t.Helper()
	path := filepath.Join(repo, "docs", "operating", "notes", "cmt-"+id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"namespace": "test-ns", "type": "cmt", "id": id,
		"instanceVersion": 1, "revision": 1,
		"author": "Reviewer", "created": "2026-08-10", "updated": "2026-08-10",
		"state": map[string]string{"contentState": "draft", "existenceState": "active", "noteState": "resolved"},
		"changeLog": []map[string]string{
			{"date": "2026-08-10", "domain": "contentState", "from": "-", "to": "draft", "by": "Reviewer"},
			{"date": "2026-08-10", "domain": "existenceState", "from": "-", "to": "active", "by": "Reviewer"},
			{"date": "2026-08-10", "domain": "noteState", "from": "-", "to": "open", "by": "Reviewer"},
			{"date": "2026-08-10", "domain": "noteState", "from": "open", "to": "resolved", "by": "Reviewer"},
		},
		"relationships": map[string][]string{"discusses": {discusses}},
		"content": map[string]any{
			"role": "review", "verdict": verdict, "notes": []string{"Looks good."},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pull from the docs tree directly (the snapshot package may be
	// current — docs mode re-analyzes the authoring files).
	if code, _, errText := runIn([]string{"sync", "pull", "--from-docs"}); code != 0 {
		t.Fatalf("seed cmt sync: exit = %d, stderr %q", code, errText)
	}
}

// noteTicketEnv builds the multi-type env plus two published cmt- notes:
// one discussing the work item sto:one, one discussing the ticket
// tkt:t-one directly.
func noteTicketEnv(t *testing.T) string {
	t.Helper()
	repo := noteEnvMulti(t)
	seedCmtNote(t, repo, "one-review", "test-ns/sto:one", "approve")
	seedCmtNote(t, repo, "t-one-review", "test-ns/tkt:t-one", "changes-requested")
	return repo
}

// TestViewTicketWithNotes: `eka view ticket --with-note` (and its
// synonym --with-comments) surfaces the notes discussing the ticket
// and its related work item — one comment card per note with the role
// tag, note-state mark, authoring metadata and the per-role content.
// Without the flag the projection is unchanged.
func TestViewTicketWithNotes(t *testing.T) {
	noteTicketEnv(t)

	for _, flag := range []string{"--with-note", "--with-comments"} {
		code, out, errText := runIn([]string{"view", "ticket", "tkt-t-one", flag})
		if code != 0 {
			t.Fatalf("view ticket %s: exit = %d, stderr %q", flag, code, errText)
		}
		for _, want := range []string{"Notes", "cmt:one-review", "cmt:t-one-review", "[review]", "resolved", "approve", "changes-requested", "test-ns/sto:one"} {
			if !strings.Contains(out, want) {
				t.Errorf("view ticket %s: stdout missing %q\n%s", flag, want, out)
			}
		}
		// Both notes are shown: the one discussing the ticket AND the
		// one discussing its related work item.
		if strings.Count(out, "cmt:") < 2 {
			t.Errorf("view ticket %s: expected 2 note cards, got:\n%s", flag, out)
		}
	}

	// Without the flag: the projection stays unchanged (no Notes).
	code, out, _ := runIn([]string{"view", "ticket", "tkt-t-one"})
	if code != 0 || strings.Contains(out, "Notes") {
		t.Errorf("view ticket without flag: exit = %d, notes section must be absent\n%s", code, out)
	}

	// A direct work item shows the notes discussing itself.
	code, out, _ = runIn([]string{"view", "ticket", "sto:one", "--with-note"})
	if code != 0 || !strings.Contains(out, "cmt:one-review") || strings.Contains(out, "cmt:t-one-review") {
		t.Errorf("view ticket sto:one --with-note: exit = %d, want only cmt:one-review\n%s", code, out)
	}

	// No notes: the section renders its informative empty state (a
	// work item without notes — the ticket projection only accepts
	// execution items).
	code, out, _ = runIn([]string{"view", "ticket", "bug:two", "--with-note"})
	if code != 0 || !strings.Contains(out, "no notes yet") {
		t.Errorf("view ticket epc:four --with-note: exit = %d, want the empty state\n%s", code, out)
	}
}

// TestGetTicketMachine: `eka get ticket <target>` emits the
// deterministic machine document (schema eka-ticket-v1); --with-notes
// (and its synonym --with-comments) appends the "notes" array of
// eka-cko-v2 Documents. Additive: without the flag no notes field.
func TestGetTicketMachine(t *testing.T) {
	noteTicketEnv(t)

	code, out, errText := runIn([]string{"get", "ticket", "tkt-t-one", "--with-notes"})
	if code != 0 {
		t.Fatalf("get ticket: exit = %d, stderr %q", code, errText)
	}
	var doc struct {
		Schema string `json:"schema"`
		Ticket struct {
			Identity   string   `json:"identity"`
			Type       string   `json:"type"`
			ID         string   `json:"id"`
			Projected  string   `json:"projected"`
			WorkItem   string   `json:"workItem"`
			Container  string   `json:"container"`
			References []string `json:"references"`
		} `json:"ticket"`
		Notes []json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("get ticket: invalid JSON: %v\n%s", err, out)
	}
	if doc.Schema != ticketSchema {
		t.Errorf("get ticket: schema = %q, want %q", doc.Schema, ticketSchema)
	}
	tk := doc.Ticket
	if tk.Identity != "test-ns/tkt:t-one" || tk.Type != "tkt" || tk.ID != "t-one" {
		t.Errorf("get ticket: ticket identity = %+v", tk)
	}
	if tk.Projected != "todo" {
		t.Errorf("get ticket: projected = %q, want todo (from sto:one)", tk.Projected)
	}
	if tk.WorkItem != "test-ns/sto:one" || tk.Container != "test-ns/ctr:w1" {
		t.Errorf("get ticket: workItem/container = %q / %q", tk.WorkItem, tk.Container)
	}
	if len(tk.References) != 2 || tk.References[0] != "test-ns/ctr:w1" {
		t.Errorf("get ticket: references = %v", tk.References)
	}
	// Notes: the ticket note AND the work-item note, as cko documents.
	if len(doc.Notes) != 2 {
		t.Fatalf("get ticket: notes = %d documents, want 2\n%s", len(doc.Notes), out)
	}
	var first struct {
		Schema   string `json:"schema"`
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(doc.Notes[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Schema != "eka-cko-v2" || first.Identity.ID != "one-review" {
		t.Errorf("get ticket: notes[0] = %+v, want the cmt:one-review cko document", first)
	}

	// --with-comments is a synonym.
	code, out2, _ := runIn([]string{"get", "ticket", "sto:one", "--with-comments", "--compact"})
	if code != 0 || !strings.Contains(out2, `"schema":"eka-ticket-v1"`) {
		t.Errorf("get ticket --with-comments --compact: exit = %d\n%s", code, out2)
	}
	var doc2 struct {
		Notes []json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out2), &doc2); err != nil {
		t.Fatal(err)
	}
	if len(doc2.Notes) != 1 {
		t.Errorf("get ticket sto:one --with-comments: notes = %d, want 1 (only cmt:one-review)", len(doc2.Notes))
	}

	// Additive contract: without the flag there is no notes field.
	code, out3, _ := runIn([]string{"get", "ticket", "tkt-t-one"})
	if code != 0 || strings.Contains(out3, `"notes"`) {
		t.Errorf("get ticket without flag: exit = %d, notes field must be absent\n%s", code, out3)
	}

	// Unknown target: usage error (exit 2).
	code, _, _ = runIn([]string{"get", "ticket", "nope"})
	if code != 2 {
		t.Errorf("get ticket nope: exit = %d, want 2", code)
	}
}
