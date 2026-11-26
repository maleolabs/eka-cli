package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/pflag"
)

// buildSchemaForTest renders the report from a fresh root command, the same
// way the command does at run time.
func buildSchemaForTest(t *testing.T) schemaReport {
	t.Helper()
	root := newRootCommand()
	return buildSchemaReport(root)
}

// TestSchemaReportCoversRegisteredCommands fails when a command exists in the
// tree but is absent from the report, which would make the machine-readable
// contract disagree with the CLI.
func TestSchemaReportCoversRegisteredCommands(t *testing.T) {
	report := buildSchemaForTest(t)
	seen := map[string]bool{}
	for _, c := range report.Commands {
		seen[c.Path] = true
	}
	root := newRootCommand()
	for _, c := range root.Commands() {
		if c.Name() == "help" || c.Name() == "completion" || c.Hidden {
			continue
		}
		if !seen[c.Name()] {
			t.Errorf("command %q is registered but absent from the schema report", c.Name())
		}
	}
}

// TestSchemaReportDeclaresMutationRisk pins the direction that matters: a
// command that writes must never be reported read-only, because that tells an
// approval layer a mutating command is safe to run unasked. The specific
// commands below each write and were misclassified while the risk was derived
// from the presentation group.
func TestSchemaReportDeclaresMutationRisk(t *testing.T) {
	report := buildSchemaForTest(t)
	byPath := map[string]schemaCommand{}
	for _, c := range report.Commands {
		byPath[c.Path] = c
	}
	mustMutate := []string{
		"new", "publish", "discard", "transition", "note", "capture",
		"edit", "relate", "retire", "sync", "project", "init",
		// Inside a Utility-grouped command but writes drafts and opens
		// GitHub issues, so it is an external side effect.
		"feedback", "feedback new", "feedback publish",
	}
	for _, path := range mustMutate {
		c, ok := byPath[path]
		if !ok {
			t.Errorf("expected %q in the report", path)
			continue
		}
		if !c.Mutates {
			t.Errorf("command %q is reported read-only but changes state", path)
		}
	}
	// Plugin-provided commands appear only when the plugin is installed, so
	// assert the classification rule rather than their presence.
	for path, c := range byPath {
		if c.Group != groupPlugins {
			continue
		}
		want := !pluginReadOnlyCommands[c.Name]
		if c.Mutates != want {
			t.Errorf("plugin command %q mutates=%v, want %v", path, c.Mutates, want)
		}
	}
	mustNotMutate := []string{"get", "context", "view", "status", "integrity", "version", "code-context"}
	for _, path := range mustNotMutate {
		c, ok := byPath[path]
		if !ok {
			t.Errorf("expected %q in the report", path)
			continue
		}
		if c.Mutates {
			t.Errorf("command %q is reported mutating but only reads", path)
		}
	}
}

// TestSchemaReportDescribesFlags pins that a flag the CLI actually accepts is
// described, and that the description is generated from the definitions rather
// than from a second list.
func TestSchemaReportDescribesFlags(t *testing.T) {
	report := buildSchemaForTest(t)
	for _, c := range report.Commands {
		if c.Path != "get" {
			continue
		}
		var found bool
		for _, f := range c.Flags {
			if f.Name == "limit" {
				found = true
				if f.Type == "" {
					t.Error("get --limit must carry its type")
				}
			}
		}
		if !found {
			t.Error("get must describe its --limit flag")
		}
		return
	}
	t.Fatal("get missing from the report")
}

// TestSchemaReportFlagsMatchDefinitions fails when the report describes a flag
// the command does not define, or omits one it does: a stale contract is worse
// than no contract, because an agent trusts it.
func TestSchemaReportFlagsMatchDefinitions(t *testing.T) {
	root := newRootCommand()
	reported := map[string]map[string]bool{}
	for _, c := range buildSchemaReport(root).Commands {
		names := map[string]bool{}
		for _, f := range c.Flags {
			names[f.Name] = true
		}
		reported[c.Path] = names
	}
	for _, c := range root.Commands() {
		if c.Name() == "help" || c.Name() == "completion" || c.Hidden {
			continue
		}
		want := map[string]bool{}
		c.Flags().VisitAll(func(f *pflag.Flag) { want[f.Name] = true })
		got := reported[c.Name()]
		for name := range want {
			if !got[name] {
				t.Errorf("command %q defines --%s but the report omits it", c.Name(), name)
			}
		}
		for name := range got {
			if !want[name] {
				t.Errorf("command %q reports --%s but does not define it", c.Name(), name)
			}
		}
	}
}

// TestSchemaReportIsDeterministic pins that two runs produce identical JSON:
// an agent caching the contract must not see it churn.
func TestSchemaReportIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	enc := json.NewEncoder(&a)
	enc.SetIndent("", "  ")
	if err := enc.Encode(buildSchemaForTest(t)); err != nil {
		t.Fatal(err)
	}
	enc = json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(buildSchemaForTest(t)); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Error("schema report is not deterministic across runs")
	}
}
