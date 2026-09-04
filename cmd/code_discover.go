package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/codegraph"
	"github.com/spf13/cobra"
)

func newCodeDiscoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code-discover <query>",
		Short: "Discover code candidates deterministically (natural query/scope -> candidates with reason/confidence)",
		Args:  cobra.RangeArgs(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, _ := cmd.Flags().GetString("scope")
			limit, _ := cmd.Flags().GetInt("limit")
			compact, _ := cmd.Flags().GetBool("compact")
			query := args[0]
			if query == "" {
				return fmt.Errorf("code-discover: query must be non-empty")
			}
			if limit < 1 || limit > codegraph.MaxCandidates {
				return fmt.Errorf("code-discover: invalid --limit %d (want 1..%d)", limit, codegraph.MaxCandidates)
			}
			root, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("code-discover: determine repository root: %w", err)
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("code-discover: determine repository root: %w", err)
			}
			root, err = codeContextRepoRoot(root)
			if err != nil {
				return fmt.Errorf("code-discover: determine repository root: %w", err)
			}
			cache, err := codeContextCachePath(root)
			if err != nil {
				return fmt.Errorf("code-discover: determine cache path: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
				return fmt.Errorf("code-discover: prepare cache: %w", err)
			}
			idx, _, err := codegraph.LoadOrBuild(root, cache)
			if err != nil {
				return fmt.Errorf("code-discover: build index: %w", err)
			}
			result, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: query, Scope: scope, Limit: limit})
			if err != nil {
				return fmt.Errorf("code-discover: discover: %w", err)
			}
			var out []byte
			if compact {
				out, err = json.Marshal(result)
			} else {
				out, err = json.MarshalIndent(result, "", "  ")
				if err == nil {
					out = append(out, '\n')
				}
			}
			if err != nil {
				return fmt.Errorf("code-discover: encode response: %w", err)
			}
			if compact {
				out = append(out, '\n')
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	cmd.Flags().String("scope", "", "optional file path scope filter")
	cmd.Flags().Int("limit", 16, "maximum candidates (1..64)")
	cmd.Flags().Bool("compact", false, "emit single-line JSON")
	return cmd
}
