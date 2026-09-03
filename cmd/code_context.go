package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/codegraph"
	"github.com/spf13/cobra"
)

func newCodeContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code-context [query]",
		Short: "Serve deterministic local code context",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			depth, _ := cmd.Flags().GetString("depth")
			level, _ := cmd.Flags().GetInt("level")
			noContent, _ := cmd.Flags().GetBool("no-content")
			compact, _ := cmd.Flags().GetBool("compact")
			limit, _ := cmd.Flags().GetInt("limit")
			if !validCodeContextDepth(depth) {
				return fmt.Errorf("code-context: invalid --depth %q (want local, dependency, or engineering)", depth)
			}
			if level < 0 || level > 3 {
				return fmt.Errorf("code-context: invalid --level %d (want 0..3)", level)
			}
			if limit < 1 || limit > codegraph.MaxUnits {
				return fmt.Errorf("code-context: invalid --limit %d (want 1..%d)", limit, codegraph.MaxUnits)
			}
			root, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("code-context: determine repository root: %w", err)
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("code-context: determine repository root: %w", err)
			}
			root, err = codeContextRepoRoot(root)
			if err != nil {
				return fmt.Errorf("code-context: determine repository root: %w", err)
			}
			cache, err := codeContextCachePath(root)
			if err != nil {
				return fmt.Errorf("code-context: determine cache path: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
				return fmt.Errorf("code-context: prepare cache: %w", err)
			}
			idx, _, err := codegraph.LoadOrBuild(root, cache)
			if err != nil {
				return fmt.Errorf("code-context: build index: %w", err)
			}
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			result, err := codegraph.Serve(idx, codegraph.Request{Focus: query, Depth: codegraph.Depth(depth), Level: level, NoContent: noContent})
			if err != nil {
				return fmt.Errorf("code-context: serve: %w", err)
			}
			if len(result.Units) > limit {
				result.Units = result.Units[:limit]
			}
			if len(result.Symbols) > limit {
				result.Symbols = result.Symbols[:limit]
			}
			if len(result.Refs) > limit {
				result.Refs = result.Refs[:limit]
			}
			var out []byte
			if compact {
				out, err = json.Marshal(result)
			} else {
				out, err = json.MarshalIndent(result, "", "  ")
				out = append(out, '\n')
			}
			if err != nil {
				return fmt.Errorf("code-context: encode response: %w", err)
			}
			if compact {
				out = append(out, '\n')
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	cmd.Flags().String("depth", "dependency", "depth local|dependency|engineering")
	cmd.Flags().Int("level", 1, "context level 0..3: inventory, symbols, imports, source")
	cmd.Flags().Bool("no-content", false, "omit source content")
	cmd.Flags().Bool("compact", false, "emit single-line JSON")
	cmd.Flags().Int("limit", 32, "maximum units, symbols, and refs (1..64)")
	return cmd
}

func validCodeContextDepth(depth string) bool {
	return depth == string(codegraph.DepthLocal) || depth == string(codegraph.DepthDependency) || depth == string(codegraph.DepthEngineering)
}

func codeContextCachePath(root string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(cacheRoot, "eka", "codegraph", hex.EncodeToString(sum[:])[:16]+".json"), nil
}

func codeContextRepoRoot(start string) (string, error) {
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "eka.yaml")); err == nil {
			return dir, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("eka.yaml not found from %s", start)
		}
	}
}
