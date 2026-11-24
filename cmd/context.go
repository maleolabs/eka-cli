package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/contexts"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// constraintsSchema is the machine schema token of the path-to-constraint
// retrieval document (`eka context --for`).
const constraintsSchema = "eka-constraints-v1"

// constraintsObject is the machine document of `eka context --for`: the
// repository-relative path and the constraints whose declared `appliesTo`
// scope binds it, in the engine's deterministic order (authority stratum
// ascending, then canonical form). It is a thin envelope over the engine's
// entries — the CLI never re-sorts or re-scopes.
type constraintsObject struct {
	Schema      string           `json:"schema"`
	Path        string           `json:"path"`
	Constraints []contexts.Entry `json:"constraints"`
}

// marshal serializes the document in the machine-style contract (compact =
// one line, pretty = two-space indented); both end in a single trailing
// newline and parse to the same object. The empty set serializes as an
// empty array, never null.
func (o constraintsObject) marshal(compact bool) ([]byte, error) {
	if o.Constraints == nil {
		o.Constraints = []contexts.Entry{}
	}
	var (
		out []byte
		err error
	)
	if compact {
		out, err = json.Marshal(o)
	} else {
		out, err = json.MarshalIndent(o, "", "  ")
	}
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// contextPathFor normalizes a --for argument to a repository-relative,
// forward-slash path — the grammar the `appliesTo` patterns are anchored
// to. A relative path is used as-is (filepath.Clean collapses '.'/'..' and
// strips a leading './'); an absolute path is relativized against the
// repository root and refused (error → usage, exit 2) when it escapes the
// repository. The empty path and "." are allowed: they bind only what the
// declarations would match (normally nothing).
func contextPathFor(root, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if filepath.IsAbs(raw) {
		rel, err := filepath.Rel(root, filepath.Clean(raw))
		if err != nil {
			return "", fmt.Errorf("context: --for %q: %w", raw, err)
		}
		sep := string(filepath.Separator)
		if rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			return "", fmt.Errorf("context: --for %q is outside the repository %s", raw, root)
		}
		return filepath.ToSlash(rel), nil
	}
	return filepath.ToSlash(filepath.Clean(raw)), nil
}

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
		Short: "Construct the engineering context around a knowledge subject, or the constraints binding a path",
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

Path-to-constraint mode (--for <path>) answers the dual question: given
a repository-relative path, which higher-authority constraints declare
that they apply to it? It reads the units' optional 'appliesTo' scope
declarations and returns exactly the matching units of this repository,
ordered by authority stratum ascending, then canonical form. One hop,
no graph expansion: a path that matches nothing yields an EMPTY set,
never an error (exit 0). The output is the deterministic Constraints
Object (schema "eka-constraints-v1"). The subject and --for are two
mutually exclusive modes: a subject together with --for is a usage
error.

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
  --for <path>   path-to-constraint mode: return the units of this
                 repository whose declared 'appliesTo' scope binds
                 <path> — the higher-authority constraints that apply
                 to it (schema "eka-constraints-v1"). The path is
                 repository-relative and forward-slashed: a relative
                 path is used as-is ('.'/'..' cleaned, a leading './'
                 stripped); an absolute path is relativized against
                 the repository root and refused when it escapes it
                 (usage, exit 2). An empty path or "." is allowed and
                 normally binds nothing. With --for the positional
                 subject must be ABSENT (subject + --for is a usage
                 error, exit 2); the empty result set is exit 0, never
                 an error. --depth does not apply to this mode.

The Constraints Object (--for, schema "eka-constraints-v1") is the
machine document {"schema","path","constraints"} — 'path' is the
normalized repository-relative path, 'constraints' the matching entry
objects in the engine's deterministic order (authority stratum
ascending, then canonical form; empty set = []). The human projection
(default) prints the path header and one line per constraint, or a
clear "no constraints bind <path>" line for the empty set.

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
  eka context feather/plan:roadmap-v1 --compact --no-content
  eka context --for src/auth/login.go
  eka context --for src/ --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			depthToken, _ := cmd.Flags().GetString(flagContextDepth)
			jsonOut, _ := cmd.Flags().GetBool(flagContextJSON)
			compact, _ := cmd.Flags().GetBool(flagContextCompact)
			noContent, _ := cmd.Flags().GetBool(flagContextNoContent)
			forPath, _ := cmd.Flags().GetString(flagContextFor)
			forMode := cmd.Flags().Changed(flagContextFor)
			// Mode exclusivity: the subject mode (positional identity) and
			// the path mode (--for <path>) are two retrieval modes of the
			// same command — never combined. A subject passed with --for,
			// or no subject without --for, is a usage error (exit 2).
			if !forMode && len(args) == 0 {
				// Machine commands never print banners: the no-argument
				// case is a usage error with the subject grammar on
				// stderr, exit 2.
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: usage: eka context <subject>")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   identity  <ns>/<type>:<id>[:<instance-version>]  (canonical form or qualified line form)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   number    #<n>  (the line's issue number)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   --for <path>  the constraints whose `appliesTo` binds <path>")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: run 'eka context --help' for the full reference")
				return &exitError{code: exitUsage}
			}
			if forMode && len(args) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: usage: eka context --for <path>  (no subject)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context:   --for and a subject are mutually exclusive modes")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: context: run 'eka context --help' for the full reference")
				return &exitError{code: exitUsage}
			}
			// Depth validation before any workspace access: an unknown
			// depth token is a pure usage error (exit 2) listing the
			// three depths.
			depth, ok := contexts.ParseDepth(depthToken)
			if !ok {
				return fmt.Errorf("context: unknown depth %q — available depths: %s", depthToken, strings.Join(contexts.Depths(), " | "))
			}
			// Subject-mode target validation, before any workspace access:
			// the subject must be a knowledge identity, never a domain
			// token or the containers query. Domain tokens and
			// "containers" get the identity grammar listed; every other
			// non-identity form is refused by the shared reference grammar
			// (conformance.ParseReference). --for mode carries no subject.
			var subject string
			if !forMode {
				subject = args[0]
				if _, isDomain := domainTokenName(subject); isDomain || subject == "containers" {
					return fmt.Errorf("context: %q is not a knowledge identity — the subject is an identity: canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> (or an issue number #<n>)", subject)
				}
				if !strings.HasPrefix(subject, "#") {
					if _, err := conformance.ParseReference(subject, "", ""); err != nil {
						return fmt.Errorf("context: %q is not a knowledge identity — canonical form <ns>/<type>:<id>:<v> or qualified line form <ns>/<type>:<id> required", subject)
					}
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
			// Path-to-constraint mode: normalize the path against the
			// repository root, then read the repository's `appliesTo`
			// declarations through the engine (repository-scoped: project
			// id + repository name). Empty set is exit 0.
			if forMode {
				rel, perr := contextPathFor(repo.Path, forPath)
				if perr != nil {
					return perr // Exit 2: path outside the repository (usage).
				}
				entries, err := contexts.New(r).UnitsForPath(repo.ProjectID, repo.Name, rel)
				if err != nil {
					return fmt.Errorf("context: %w", err) // Exit 2: declaration read failure.
				}
				out := constraintsObject{Schema: constraintsSchema, Path: rel, Constraints: entries}
				if jsonOut || compact {
					// Output contract: stdout carries ONLY the Constraints
					// Object plus its single trailing newline.
					b, err := out.marshal(compact)
					if err != nil {
						return fmt.Errorf("context failed: %w", err)
					}
					if _, err := cmd.OutOrStdout().Write(b); err != nil {
						return fmt.Errorf("context failed: %w", err)
					}
					return nil
				}
				renderConstraints(styleFor(cmd), out)
				return nil
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
	cmd.Flags().String(flagContextFor, "", "path-to-constraint mode: the units whose `appliesTo` scope binds <path> (schema eka-constraints-v1); the subject must be absent")
	cmd.Flags().Bool(flagContextJSON, false, "emit the Context Object as JSON on stdout (the machine-readable form)")
	cmd.Flags().Bool(flagContextCompact, false, "emit the JSON as a single line (plus trailing newline); implies --json")
	cmd.Flags().Bool(flagContextNoContent, false, "strip the focus content payload from the Context Object")
	cmd.Flags().Bool(flagContextNoHistory, false, "strip history section from Context Object (additive thin flag)")
	cmd.Flags().String(flagContextOnlySection, "", "only include one section: constraints|dependencies|decisions|planning|review|upstream|downstream (additive thin flag)")
	cmd.Flags().Int(flagContextMaxUnits, 0, "truncate to first N units by canonicalForm, envelope truncated:true (additive thin flag)")
	cmd.Flags().Int(flagContextMaxBytes, 0, "truncate to N bytes estimated, envelope truncated:true (additive thin flag)")
	return cmd
}

// Flag names of the context options (declared once, shared by the
// help text and the flag lookups).
const (
	flagContextDepth       = "depth"
	flagContextFor         = "for"
	flagContextJSON        = "json"
	flagContextCompact     = "compact"
	flagContextNoContent   = "no-content"
	flagContextNoHistory   = "no-history"
	flagContextOnlySection = "only-section"
	flagContextMaxUnits    = "max-units"
	flagContextMaxBytes    = "max-bytes"
)
