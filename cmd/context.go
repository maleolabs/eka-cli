package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/contexts"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newContextCommand builds the `eka context` command: the Context
// Engine interface of the EKA Runtime. It constructs the deterministic
// Engineering Context Object (schema "eka-context-v1") around ONE
// knowledge subject — the engineering lens of the unit: full focus
// detail, the higher-authority constraints, the one-hop neighborhood
// classified into sections, the strata landscape and the focus's
// instance-line history. Purely deterministic — no LLM, no randomness:
// the same subject, depth and options always produce the same context.
//
// The Context Engine is a Runtime consumer (ADR-014): it consumes
// Engineering Knowledge ONLY through the runtime package services
// (Resolver, Relations, Timeline, Knowledge). It never parses
// Markdown and never touches storage — the repository must be synced
// first ('eka sync').
//
// Subject grammar (the Resolver grammar):
//
//	<ns>/<type>:<id>:<v>   the canonical form — the exact instance
//	<ns>/<type>:<id>       the qualified line form — the highest
//	                       instance-version of the line (the latest
//	                       knowledge version, ADR-025)
//	#<n>                   the line's issue number (RFC: per-group
//	                       incremental numbers, unambiguous across the
//	                       per-group counters)
//
// The namespace is required (the Runtime resolves globally;
// unqualified forms are refused).
//
// Depths:
//
//	local        the object itself plus its instance-line history —
//	             no relationships collected
//	dependency   the one-hop neighborhood: upstream (units the focus
//	             references), downstream (units referencing the focus),
//	             dependencies (depends-on / derives-from targets),
//	             classified into sections (constraints by higher
//	             authority stratum, decisions/planning/review by type
//	             token) and the strata landscape
//	engineering  the dependency context PLUS a bounded constraint
//	             closure: higher-authority units (strata above the
//	             focus) reachable through the collected units' own
//	             relationships (max 2 hops, at most 64 units)
//
// Exit codes:
//
//	0  context constructed
//	1  workspace/repository-state refusal (no workspace, the current
//	   directory is not an EKA repository — no eka.yaml — or the
//	   repository is not registered)
//	2  usage or internal error (invalid subject or depth, unknown
//	   identity, resolver/store failure)
//
// Relationship with the other Runtime consumers: 'eka get' retrieves
// the canonical knowledge (the machine interface), 'eka view'
// visualizes it (the projection interface) — 'eka context' constructs
// the engineering context around a subject (the context interface).
// All three are Runtime consumers: they read the synced canonical
// store through the runtime package and never parse Markdown.
func newContextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context <subject>",
		Short: "Construct the engineering context around a knowledge subject",
		Long: `Construct the Engineering Context around one knowledge subject —
the Context Engine interface of the EKA Runtime. Where 'eka get'
retrieves canonical knowledge and 'eka view' renders projections for
reading, 'eka context' constructs the engineering context of a unit:
the focus (full unit detail), the higher-authority constraints, the
one-hop neighborhood classified into sections (upstream, downstream,
dependencies, decisions, planning, review), the strata landscape and
the focus's instance-line history. The output is the deterministic
Context Object (schema "eka-context-v1") or its human projection —
purely deterministic, no LLM, no randomness.

The Context Engine is a Runtime consumer (ADR-014): it consumes
Engineering Knowledge ONLY through the runtime package services
(Resolver, Relations, Timeline, Knowledge) — it never parses Markdown
and never touches storage. The repository must be synced first
('eka sync').

The subject is a knowledge identity:

  <ns>/<type>:<id>[:<v>]
        the RSF canonical form (the exact instance) or the qualified
        line form (the highest instance-version of the line — the
        latest knowledge version, ADR-025). The
        namespace is required: the Runtime resolves globally, so
        unqualified forms are refused.
  #<n>  the line's issue number (RFC: per-group incremental numbers,
        GitHub-style; work items, tickets and notes count
        independently, so the number must be unambiguous across the
        per-group counters).

Depths (--depth):

  local         the object itself plus its instance-line history —
                no relationships are collected (one stratum: the
                focus's own).
  dependency    the one-hop neighborhood: upstream (the units the
                focus's relationships point at), downstream (the
                units that reference the focus), dependencies (the
                depends-on / derives-from outgoing targets) —
                classified into sections: constraints (collected
                units of strictly higher authority strata — a
                strictly smaller stratum number), decisions
                (adr-/dec-), planning (scp-/epc-/plan-/trc-) and
                review (rvw-/cmt-), plus the strata landscape of the
                collected units.
  engineering   the dependency context PLUS a bounded constraint
                closure: higher-authority units reachable through the
                collected units' own outgoing relationships (max 2
                hops, at most 64 units). Hop-2 units join the
                constraints section with the role "constraint".

Flags:

  --json         emit the Context Object as JSON on stdout — the
                 machine-readable form (schema "eka-context-v1",
                 deterministic: fixed field order, sections sorted by
                 canonical form, no timestamps, no host-dependent
                 values).
  --compact      emit the JSON as a single line (plus trailing
                 newline) instead of the indented form — same
                 object, same field order, fewer bytes. Implies
                 --json.
  --no-content   strip the focus content payload: the "content" key
                 is absent from the JSON. Token-saving for consumers
                 that only need identity, state and relationships.

The human projection (default, no --json) renders the context as the
context header, the classified sections, the strata landscape, the
history and the summary — the reading interface of the Context
Engine.

The repository must be an EKA repository — a directory tree carrying
eka.yaml (run 'eka init' to create one) — registered in the EKA
workspace and synced first ('eka sync'). Run this command inside the
repository root.

Output contract: with --json/--compact, stdout carries ONLY the JSON
object followed by a single trailing newline. No banners, no
informational lines: machine consumers parse stdout verbatim. Errors
go to stderr, one 'eka: ...' line per error (the bare 'eka context'
usage summary is the exception and also goes to stderr).

Exit codes:
  0  context constructed
  1  workspace/repository-state refusal (no EKA workspace,
     repository not registered in the workspace)
  2  usage or internal error (invalid subject or depth, unknown
     identity, resolver failure)`,
		Example: `  eka context feather/sto:publish-post
  eka context feather/adr:001-login-serialization --depth engineering
  eka context #42 --json
  eka context feather/plan:roadmap-v1 --compact --no-content`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			depthToken, _ := cmd.Flags().GetString(flagContextDepth)
			jsonOut, _ := cmd.Flags().GetBool(flagContextJSON)
			compact, _ := cmd.Flags().GetBool(flagContextCompact)
			noContent, _ := cmd.Flags().GetBool(flagContextNoContent)
			if len(args) == 0 {
				// Machine commands never print banners: the no-argument
				// case is a usage error with the subject grammar on
				// stderr, exit 2.
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: usage: eka context <subject>")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   identity  <ns>/<type>:<id>[:<instance-version>]  (canonical form or qualified line form)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   number    #<n>  (the line's issue number)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: run 'eka context --help' for the full reference")
				return &exitError{code: exitUsage}
			}
			subject := args[0]
			// Depth validation before any workspace access: an unknown
			// depth token is a pure usage error (exit 2) listing the
			// three depths.
			depth, ok := contexts.ParseDepth(depthToken)
			if !ok {
				return fmt.Errorf("context: unknown depth %q — available depths: %s", depthToken, strings.Join(contexts.Depths(), " | "))
			}
			// Target validation before any workspace access: the
			// subject must be a knowledge identity, never a domain
			// token or the containers query. Domain tokens and
			// "containers" get the identity grammar listed; every
			// other non-identity form is refused by the shared
			// reference grammar (conformance.ParseReference).
			if _, isDomain := domainTokenName(subject); isDomain || subject == "containers" {
				return fmt.Errorf("context: %q is not a knowledge identity — the subject is an identity: canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> (or an issue number #<n>)", subject)
			}
			if !strings.HasPrefix(subject, "#") {
				if _, err := conformance.ParseReference(subject, "", ""); err != nil {
					return fmt.Errorf("context: %q is not a knowledge identity — canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", subject)
				}
			}
			// The resolution prologue: open (never create) the Runtime,
			// then gate on workspace and repository state.
			r, err := runtime.Open()
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()
			if !r.Exists() {
				// Workspace-state refusal: `eka context` never creates
				// a workspace — deterministic message, exit 1.
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: context refused: no EKA workspace at %s; run 'eka sync' first\n", r.Path())
				return &exitError{code: exitFail}
			}
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("context failed: %w", err)
			}
			abs = filepath.Clean(abs)
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the refusal replaces
			// the not-registered branch (exit 1, the same refusal
			// class).
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("context failed: %w", err) // Exit 2: metadata read failure.
			}
			if !hasMeta {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: context refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first\n", abs)
				return &exitError{code: exitFail}
			}
			repo, found, err := r.Workspace.FindRepo(abs)
			if err != nil {
				return fmt.Errorf("context failed: %w", err) // Exit 2: registry failure.
			}
			if !found {
				// Repository-state refusal: deterministic message and
				// exit 1 — no context is produced.
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: context refused: repository %s is not registered in the EKA workspace; run 'eka sync' (auto-registers) or 'eka project register' first\n", abs)
				return &exitError{code: exitFail}
			}
			// Issue-number subjects (RFC): "#<n>" resolves to its line
			// (unambiguous across the per-group counters) before the
			// context construction.
			if strings.HasPrefix(subject, "#") {
				resolved, rerr := resolveNumberTarget(r, repo.ProjectID, subject, "")
				if rerr != nil {
					return rerr
				}
				subject = resolved
			}
			obj, err := contexts.New(r).Build(subject, repo.ProjectID, depth, contexts.Options{NoContent: noContent})
			if err != nil {
				return fmt.Errorf("context: %w", err) // Exit 2: usage/internal.
			}
			if jsonOut || compact {
				// Output contract: stdout carries ONLY the JSON object
				// plus its single trailing newline (Marshal emits it) —
				// written verbatim, never re-rendered.
				var out []byte
				if compact {
					out, err = obj.MarshalCompact()
				} else {
					out, err = obj.Marshal()
				}
				if err != nil {
					return fmt.Errorf("context failed: %w", err)
				}
				if _, err := cmd.OutOrStdout().Write(out); err != nil {
					return fmt.Errorf("context failed: %w", err)
				}
				return nil
			}
			// Human projection: the context renderer (never builds
			// context itself — a thin renderer over the object).
			renderContext(styleFor(cmd), obj, repo.ProjectID)
			return nil
		},
	}
	cmd.Flags().String(flagContextDepth, string(contexts.DepthDependency), "context depth: local | dependency | engineering (default dependency)")
	cmd.Flags().Bool(flagContextJSON, false, "emit the Context Object as JSON on stdout (the machine-readable form)")
	cmd.Flags().Bool(flagContextCompact, false, "emit the JSON as a single line (plus trailing newline); implies --json")
	cmd.Flags().Bool(flagContextNoContent, false, "strip the focus content payload from the Context Object")
	return cmd
}

// Flag names of the context options (declared once, shared by the
// help text and the flag lookups).
const (
	flagContextDepth     = "depth"
	flagContextJSON      = "json"
	flagContextCompact   = "compact"
	flagContextNoContent = "no-content"
)
