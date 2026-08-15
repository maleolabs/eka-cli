package cmd

import (
	"bytes"
	"testing"

	"github.com/maleolabs/eka-cli/cmd/ui"
)

// TestRenderUsageMatchesCobraUsage locks the CLI help renderer against
// cobra's own usage layout: renderUsage with a non-color Style must be
// byte-identical to cobra's UsageString (which renders through cobra's
// default usage template) for the same command state. Cobra's layout is
// an internal contract — this test fails the build the moment either
// side drifts. InitDefaultHelpFlag mirrors what Execute does before
// help renders, so the -h/--help flag is visible to both sides.
func TestRenderUsageMatchesCobraUsage(t *testing.T) {
	root := newRootCommand()
	root.InitDefaultHelpFlag()

	cmds := []string{"" /* root */, "get"}
	for _, name := range cmds {
		cmd := root
		if name != "" {
			cmd = findCommand(root, name)
			if cmd == nil {
				t.Fatalf("command %q not registered", name)
			}
		}
		var buf bytes.Buffer
		renderUsage(cmd, &ui.Style{Color: false}, &buf)
		if got, want := buf.String(), cmd.UsageString(); got != want {
			t.Errorf("%s: renderUsage diverges from cobra's usage layout:\n--- got ---\n%s\n--- want (cobra UsageString) ---\n%s",
				cmd.Name(), got, want)
		}
	}
}
