package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/codegraph"
	"github.com/spf13/cobra"
)

func newCodeGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code-get <path>",
		Short: "Retrieve exact file content deterministically",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compact, _ := cmd.Flags().GetBool("compact")
			path := args[0]
			if path == "" {
				return fmt.Errorf("code-get: path must be non-empty")
			}
			root, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("code-get: determine repository root: %w", err)
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("code-get: determine repository root: %w", err)
			}
			root, err = codeContextRepoRoot(root)
			if err != nil {
				return fmt.Errorf("code-get: determine repository root: %w", err)
			}
			cache, err := codeContextCachePath(root)
			if err != nil {
				return fmt.Errorf("code-get: determine cache path: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
				return fmt.Errorf("code-get: prepare cache: %w", err)
			}
			idx, _, err := codegraph.LoadOrBuild(root, cache)
			if err != nil {
				return fmt.Errorf("code-get: build index: %w", err)
			}
			result, err := codegraph.Get(idx, codegraph.GetRequest{Path: path})
			if err != nil {
				return fmt.Errorf("code-get: get: %w", err)
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
				return fmt.Errorf("code-get: encode response: %w", err)
			}
			if compact {
				out = append(out, '\n')
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	cmd.Flags().Bool("compact", false, "emit single-line JSON")
	return cmd
}
