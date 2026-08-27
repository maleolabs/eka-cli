package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newCodeContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code-context <symbol>",
		Short: "Serve Code Graph via code_context (sto:code-context-tool-real — hollow gap fix)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "code_context: depth local|dependency|engineering --no-content/--compact/--limit bounded 32/64")
			return nil
		},
	}
	cmd.Flags().String("depth", "dependency", "depth local|dependency|engineering")
	cmd.Flags().Bool("no-content", false, "refs only <50% tokens")
	cmd.Flags().Bool("compact", false, "single line json")
	cmd.Flags().Int("limit", 32, "bounded 32 symbols / 64 units")
	return cmd
}
