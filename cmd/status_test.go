package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/runtime"
)

// TestStatusNoWorkspace: without a workspace.json, status prints an
// informational message and exits 0 — it never initializes the
// workspace.
func TestStatusNoWorkspace(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	code, text, errText := runIn([]string{"status"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	if !strings.Contains(text, "No EKA workspace") {
		t.Errorf("status must explain the missing workspace:\n%s", text)
	}
	if !strings.Contains(text, "eka project register") {
		t.Errorf("status must point at the initialization command:\n%s", text)
	}
	// The workspace must not have been created.
	code, text2, _ := runIn([]string{"status"})
	if text != text2 {
		t.Error("status output differs between runs")
	}
}

func TestStatusHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"status", "-h"}, {"status", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka status") {
			t.Errorf("args %v: help must mention eka status", args)
		}
	}
}

// TestStatusWorkspaceUninitializedNeverCreates: the informational
// status must not leave a workspace behind.
func TestStatusWorkspaceUninitializedNeverCreates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	if code, _, _ := runIn([]string{"status"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("status must not create workspace files, found: %v", entries)
	}
}

// TestStatusInitializedNoProjects: an initialized workspace with no
// registered projects shows the informational note and exits 0.
func TestStatusInitializedNoProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	code, text, _ := runIn([]string{"status"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(text, "No projects registered") {
		t.Errorf("status must show the no-projects note, got:\n%s", text)
	}
	if !strings.Contains(text, "Objects: 0") {
		t.Errorf("status must show zero counts, got:\n%s", text)
	}
}
