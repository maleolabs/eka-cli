package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

func newShareCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Interactive shr sharing (EKA and non-EKA)",
		Long: `Interactive Q&A for sharing knowledge as shr.
Asks level, identifier (project+version), provenance, title, export choice.
Works for EKA (eka.yaml) and non-EKA (filesystem path) — deep audit adapts to target level.
Skills: eka-shr-builder (EKA) and eka-shr-non-eka (non-EKA deep audit) — both English, eka- prefix.

Flags are optional; when TTY, unprovided values are prompted interactively.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			isTTY := isTerminal(os.Stdin)
			level, _ := cmd.Flags().GetString("level")
			project, _ := cmd.Flags().GetString("project")
			version, _ := cmd.Flags().GetString("version")
			provenance, _ := cmd.Flags().GetString("provenance")
			title, _ := cmd.Flags().GetString("title")
			source, _ := cmd.Flags().GetString("source")
			reader := bufio.NewReader(os.Stdin)
			ask := func(prompt, def string) string {
				if !isTTY {
					return def
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s [%s]: ", prompt, def)
				line, _ := reader.ReadString('\n')
				line = strings.TrimSpace(line)
				if line == "" {
					return def
				}
				return line
			}
			if level == "" {
				level = ask("Level (L0/L1/L2)", "L0")
			}
			level = strings.ToUpper(strings.TrimSpace(level))
			if source == "" {
				source = ask("Source (CKO identity or filesystem path for non-EKA)", "eka/adr:example")
			}
			if project == "" {
				project = ask("Project (from eka.yaml or ask for non-EKA)", "eka")
			}
			if version == "" {
				version = ask("Version (semver)", "1.0.0")
			}
			if provenance == "" {
				provenance = ask("Provenance (extracted/audited)", "extracted")
			}
			if title == "" {
				title = ask("Title", fmt.Sprintf("Share %s %s", level, source))
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Interactive Share").
				Add("Level", level).
				Add("Source", source).
				Add("Project", project).
				Add("Version", version).
				Add("Provenance", provenance).
				Add("Title", title).
				Pipeline("Share").
				Render()
			// Delegate to shr build stub suggestion
			fmt.Fprintf(cmd.OutOrStdout(), "Next: eka shr build %s --level %s --project %s --title %q\n", source, level, project, title)
			_ = isTTY
			return nil
		},
	}
	cmd.Flags().String("level", "", "L0|L1|L2")
	cmd.Flags().String("project", "", "project identifier")
	cmd.Flags().String("version", "", "version (semver)")
	cmd.Flags().String("provenance", "", "extracted|audited")
	cmd.Flags().String("title", "", "shr title")
	cmd.Flags().String("source", "", "source CKO identity or filesystem path")
	return cmd
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
