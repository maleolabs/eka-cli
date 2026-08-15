package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// This file implements the authoring commands of the draft-publish
// workflow (reference/spec-authoring-publish.md §4): eka new, eka
// edit, eka draft list, eka draft validate, eka publish, eka discard.
// The commands are thin Cobra layers over the Authoring API
// (runtime.Authoring): they resolve targets/namespaces/projects, run
// the editor, render and map exit codes — no draft storage, no store
// access.
//
// Exit codes (spec §4):
//
//	new     0 created; 1 refused (collision, unresolvable namespace,
//	        invalid target, --edit without a terminal, not an EKA
//	        repository); 2 usage/internal
//	edit    0 edited and re-validated; 2 refused (non-TTY, published
//	        form, draft not found, not an EKA repository)
//	draft list     0 (also when empty — informational); 2 internal
//	draft validate 0 valid; 1 validation findings, malformed draft,
//	        or draft not found; 2 usage/internal
//	publish 0 published; 1 validation failure, malformed draft, draft
//	        not found, or not an EKA repository; 2 usage/internal
//	discard 0 discarded; 2 draft not found / usage (non-TTY without
//	        --force, not an EKA repository)
//
// Namespace resolution (spec §3.2, D6): a qualified target
// "<ns>/<type>:<id>" must carry the repository's namespace (a
// different namespace is refused — cross-platform access is
// read-only, qualified reads stay unchanged); inside a registered
// repository an unqualified target resolves to repos.namespace;
// outside one it is refused with the spec's hint.
//
// Workspace-native authoring (sto:workspace-native-authoring): `eka
// new` also targets a project/namespace EXPLICITLY with --project and
// --namespace — the drafts of the workspace live under
// EKA_HOME/drafts/<project>/, so a draft can be scaffolded against any
// project REGISTERED in the workspace (run 'eka project list') from
// ANY directory, inside or outside a repository. The explicit path is
// a deliberate step outside the repository identity: it never borrows
// project/namespace from eka.yaml (ADR-017 D6 rejected --project as a
// REPO OVERRIDE because an override that can differ from the immutable
// identity is ambiguous; the explicit pair --project + --namespace is
// the cross-project integration that D6's internal query slot kept the
// door open for — deterministic, no ambiguity, and only for registered
// projects). Publishing workspace-native drafts still requires a
// repository context (the ADR-018 gate of publish/edit/discard
// unchanged; the cross-project draft fallback resolves the draft from
// any project).
//
// Repository context (ADR-018): an EKA repository is a directory tree
// carrying eka.yaml. All four mutating commands (new, publish, edit,
// discard) refuse deterministically when the walk-up finds no eka.yaml
// — run 'eka init' first; there is no legacy mode. The ADR-018 gate
// applies to the repository-context path only: an explicit
// --project/--namespace target needs no repository at all.

// Flag names of the authoring commands (declared once, shared by the
// help text and the flag lookups).
const (
	flagProject          = "project"
	flagNewNamespace     = "namespace"
	flagNewDimension     = "dimension"
	flagNewPhase         = "phase"
	flagNewDependsOn     = "depends-on"
	flagNewDerivesFrom   = "derives-from"
	flagNewValidates     = "validates"
	flagNewSupersedes    = "supersedes"
	flagNewAmends        = "amends"
	flagNewContentFile   = "content-file"
	flagNewBatchFile     = "file"
	flagNewEdit          = "edit"
	flagNewBy            = "by"
	flagNewByKind        = "by-kind"
	flagPublishVersion   = "instance-version"
	flagPublishAll       = "all"
	flagPublishPending   = "pending"
	flagDiscardForce     = "force"
	flagDraftListProject = "project"
)

// newAuthoringCommands builds the seven authoring commands (new, edit,
// draft, publish, discard and relate; relate is an authoring command —
// a relationship-only edge-add — not a draft-publish command).
func newAuthoringCommands() []*cobra.Command {
	return []*cobra.Command{
		newNewCommand(),
		newEditCommand(),
		newDraftCommand(),
		newPublishCommand(),
		newDiscardCommand(),
		newRelateCommand(),
	}
}

// isTTYReader reports whether r is a terminal: an *os.File whose
// underlying fd answers true to term.IsTerminal (the stdin-side
// counterpart of ui.IsTTY). Any other reader (bytes.Buffer,
// strings.Reader, a pipe) is not a terminal — interactive commands
// (--edit, eka edit, the discard prompt) are refused.
func isTTYReader(r interface{ Read([]byte) (int, error) }) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// refuse renders a deterministic refusal on stderr and returns the
// exit-1 exitError (the "refused" class of the authoring commands).
func refuse(cmd *cobra.Command, format string, args ...any) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: "+format+"\n", args...)
	return &exitError{code: exitFail}
}

// parseDraftTarget parses a draft target ("<ns>/<type>:<id>" or
// "<type>:<id>") and refuses canonical published forms (carrying an
// instance-version suffix): there is no edit/discard/publish path to an
// immutable CKO.
func parseDraftTarget(target string) (conformance.Reference, error) {
	ref, err := conformance.ParseReference(target, "", "")
	if err != nil {
		return ref, fmt.Errorf("invalid draft target %q: %w", target, err)
	}
	if ref.HasVersion {
		return ref, fmt.Errorf("%s is a published knowledge object; drafts only", target)
	}
	return ref, nil
}

// openAuthoringRuntime opens (creating when missing) the EKA Runtime
// for the mutating authoring commands: drafts live in the workspace,
// so `eka new`/`eka publish`/`eka edit`/`eka discard` always work
// against an initialized workspace.
func openAuthoringRuntime(cmd *cobra.Command) (*runtime.Runtime, error) {
	r, err := runtime.Ensure()
	if err != nil {
		return nil, err // Exit 2: workspace resolution.
	}
	return r, nil
}

// --- eka new -----------------------------------------------------------

// newNewCommand builds `eka new <target>`: scaffold a draft from the
// deterministic JSON template. Target: "<ns>/<type>:<id>" or
// "<type>:<id>".
func newNewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <target> | --file <batch.json>",
		Short: "Scaffold a draft (or a batch of drafts)",
		Long: `Scaffold a draft: the deterministic JSON authoring template written to
<workspace>/drafts/<project>/<type>-<id>.json (spec-standard-v2 §8).

The target is the draft identity: <ns>/<type>:<id> (qualified — the
namespace must equal the repository's namespace; a different
namespace is refused, cross-platform access is read-only) or
<type>:<id> (unqualified — resolved per the spec §3.2 rules: inside
a registered repository the repository's default namespace applies
(the identity comes from eka.yaml); outside one, an unqualified
target is refused with a hint).

The command requires an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — there is no legacy mode, so
outside an EKA repository the command is refused), unless the scope is
given explicitly: --project + --namespace target a project REGISTERED
in the workspace (run 'eka project list') from any directory —
workspace-native authoring, no repository needed. An explicit --project
requires --namespace (or a qualified target) and must name a registered
project; an explicit --namespace alone resolves the project from the
repository context. The explicit path never reads eka.yaml: the draft's
project/namespace come from the flags or the qualified target, and the
cross-platform (D6) refusal does not apply to a deliberate explicit
target. The draft lands under EKA_HOME/drafts/<project>/ and is
published like any other draft: 'eka publish' (and edit/discard) still
require a repository context, resolving the draft through the
cross-project fallback.

The template carries the full §3.2 schema (namespace, type, id,
revision 1, the type's owned state fields with their initial values,
dimension/phase when given, the relationship fields when given, the
change-log covering every owned domain and the phase context when
given) plus the type's required
content keys as empty placeholders. instanceVersion is deliberately
absent — it is assigned at publish time.

A container draft (ctr-) requires --depends-on with a plan-
reference: activating the container locks its depends-on plan
(planning-state -> immutable) atomically with the activation
(protocol §4), so a container without a plan can never publish or
activate.

Batch authoring (--file <batch.json>): scaffold a SET of related
drafts in one invocation — the batch form of the same scaffold. The
file is a JSON object:

  {
    "drafts": [
      {"type": "plan", "id": "roadmap-v2", "dimension": "planning",
       "phase": "mvp",
       "relationships": {"derivesFrom": ["scp:product-v1"],
                         "dependsOn": ["scp:product-v1"]},
       "content": {"objective": "...", "scope": "...", "outOfScope": "..."}},
      {"type": "ctr", "id": "wave-7",
       "relationships": {"dependsOn": ["plan:roadmap-v2"]}},
      {"type": "sto", "id": "item-1",
       "relationships": {"dependsOn": ["ctr:wave-7"]}},
      {"type": "tkt", "id": "ticket-1",
       "relationships": {"derivesFrom": ["ctr:wave-7", "sto:item-1"]}}
    ]
  }

Per target: "type" and "id" are required; "dimension" and "phase"
are optional (phase is scp-/plan- only, the same rule as --phase);
"relationships" maps the five relationship keys — dependsOn,
derivesFrom, validates, supersedes, amends — each to an array of
target references (the same targets the single-target flags accept;
a tkt- target must derive from a ctr-, a ctr- target must depend on
a plan-); "content" is an object merged over the type's required-
section placeholders (the per-target form of --content-file).
contentState is not a batch field: every draft scaffolds at its
type's default state.

All targets are scaffolded in the resolved project and namespace (the
repository's, or the explicit --project/--namespace). The batch is
all-or-nothing: when any target cannot be scaffolded (a collision, an
unknown type, a guard violation), the run refuses and removes the
drafts it created — no partial set.

--file is mutually exclusive with the single-target flags: combining
it with --dimension, --phase, any relationship flag
(--depends-on/--derives-from/--validates/--supersedes/--amends),
--content-file or --edit is a usage error — per-target values belong
in the batch file (--by/--by-kind stay legal: the batch-wide
change-log authority).

Flags:
  --file <path>         scaffold the batch in <path> instead of a
                        single target
  --project <name>      explicit project for workspace-native authoring:
                        a project REGISTERED in the workspace (run 'eka
                        project list'), targeted from any directory;
                        requires --namespace or a qualified target
  --namespace <ns>      explicit namespace for workspace-native
                        authoring: overrides the repository/target
                        namespace (--project, when given, must be
                        registered)
  --dimension <token>    primary knowledge dimension (knowledge types)
  --phase <value>        phase context (scp-/plan- only)
  --depends-on <ref>[,<ref>...]   relationship targets (also
  --derives-from, --validates, --supersedes, --amends); comma-joined
                         values and repeated flags accumulate; ctr-
                         drafts require a plan- depends-on reference
  --content-file <path>  prepopulate the draft content from a JSON
                         object file (agents); the object is merged
                         into the draft's content (raw text is
                         rejected for JSON drafts)
  --by <name>            change-log authority name (default: ` + "`git config user.name`" + `)
  --by-kind <kind>       author identity kind: user, agent, or worker
                         (default: user)
  --edit                 TTY only: open $EDITOR (fallback vi) on the
                         draft after scaffolding, then re-validate

Exit codes:
  0  draft(s) created
  1  refused (collision, unresolvable namespace, invalid target,
     malformed batch file, --edit without a terminal, not an EKA
     repository)
  2  usage or internal error`,
		Example: `  eka new feather/sto:my-item
  eka new tkt:wave-7-item-1 --derives-from ctr:wave-7,sto:my-item
  eka new adr:001 --content-file proposal.json
  eka new plan:roadmap-v2 --phase milestone
  eka new sto:my-item --project atrium --namespace atrium-api
  eka new atrium-api/sto:my-item --project atrium
  eka new --file batch.json
  eka new --file batch.json --project atrium --namespace atrium-api`,
		Args: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString(flagNewBatchFile)
			if file != "" {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Batch authoring: --file scaffolds a set of drafts in one
			// invocation (no positional target). --edit is TTY-bound
			// and single-draft only — a batch is authored through its
			// content and edited individually afterwards. The
			// single-target flags (--dimension, --phase, the
			// relationship flags, --content-file) are meaningless on
			// the batch path and are refused instead of silently
			// dropped: per-target values belong in the batch file.
			if file, _ := cmd.Flags().GetString(flagNewBatchFile); file != "" {
				if edit, _ := cmd.Flags().GetBool(flagNewEdit); edit {
					return newUsage(cmd, "new: --edit is not available with --file; author batch drafts through the batch content, then edit individually")
				}
				if conflict := batchFlagConflict(cmd); conflict != "" {
					return newUsage(cmd, fmt.Sprintf("new: --%s is a single-target flag and is not available with --file; per-target values belong in the batch file", conflict))
				}
				byFlag, _ := cmd.Flags().GetString(flagNewBy)
				byKindFlag, _ := cmd.Flags().GetString(flagNewByKind)
				by, err := runtime.BySource(byFlag, byKindFlag, ".")
				if err != nil {
					return newUsage(cmd, err.Error())
				}
				r, err := openAuthoringRuntime(cmd)
				if err != nil {
					return err
				}
				defer r.Close()
				projectFlag, _ := cmd.Flags().GetString(flagProject)
				nsFlag, _ := cmd.Flags().GetString(flagNewNamespace)
				return runNewBatch(cmd, r, by, file, projectFlag, nsFlag)
			}

			ref, err := parseDraftTarget(args[0])
			if err != nil {
				return refuse(cmd, "new: %v", err)
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			// --edit is TTY-only: the refusal happens BEFORE the
			// scaffold, so a refused run leaves no draft file behind
			// (agents use --content-file or inline publish).
			edit, _ := cmd.Flags().GetBool(flagNewEdit)
			if edit && !isTTYReader(cmd.InOrStdin()) {
				return refuse(cmd, "new: --edit requires a terminal; use --content-file for non-interactive authoring")
			}

			// The change-log authority (by): --by, else `git config
			// user.name` — the same BySource resolution `eka note` and
			// `eka transition` use, so every authoring command defaults
			// to the same identity (an unresolved source is a usage
			// error, never a silent fallback).
			byFlag, _ := cmd.Flags().GetString(flagNewBy)
			byKindFlag, _ := cmd.Flags().GetString(flagNewByKind)
			by, err := runtime.BySource(byFlag, byKindFlag, ".")
			if err != nil {
				return newUsage(cmd, err.Error())
			}

			projectFlag, _ := cmd.Flags().GetString(flagProject)
			nsFlag, _ := cmd.Flags().GetString(flagNewNamespace)
			project, ns, err := resolveNewScope(r, ref, projectFlag, nsFlag)
			if err != nil {
				return refuse(cmd, "new: %v", err)
			}
			dimension, _ := cmd.Flags().GetString(flagNewDimension)
			phase, _ := cmd.Flags().GetString(flagNewPhase)
			contentFile, _ := cmd.Flags().GetString(flagNewContentFile)
			draft, err := runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
				Project:       project,
				Namespace:     ns,
				Type:          ref.Type,
				ID:            ref.ID,
				Dimension:     dimension,
				Phase:         phase,
				By:            by,
				Relationships: collectRelationships(cmd),
				ContentFile:   contentFile,
			})
			if err != nil {
				return refuse(cmd, "new: %v", err)
			}

			s := styleFor(cmd)
			ui.NewHeader(s, "Draft").
				Add("Project", draft.Project).
				Add("Identity", ns+"/"+ref.Type+":"+ref.ID).
				Pipeline("New").
				Render()
			ui.NewSummary(s).
				Add("Draft", fmt.Sprintf("%s:%s", ref.Type, ref.ID)).
				Add("Path", draft.Path).
				Add("Next", "eka edit to fill, eka publish to persist").
				Render()

			if edit {
				if err := runEditor(draft.Path); err != nil {
					return fmt.Errorf("new: editor failed: %w", err)
				}
				// CKO-level re-validation (spec §4.2): the draft was
				// already scaffolded, so findings are reported, never
				// destructive.
				dv, err := runtime.Authoring.ValidateDraft(r, args[0], project)
				if err != nil {
					printDraftValidationError(s, args[0], err)
				} else {
					if dv.Ref.Note != "" {
						fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info(dv.Ref.Note))
					}
					printCKOReport(s, args[0], dv.Report)
				}
			}
			return nil
		},
	}
	cmd.Flags().String(flagNewDimension, "", "primary knowledge dimension (knowledge types)")
	cmd.Flags().String(flagNewPhase, "", "phase context (scp-/plan- only)")
	// Relationship targets: StringSlice — repeated occurrences and
	// comma-joined values accumulate (never silently override).
	cmd.Flags().StringSlice(flagNewDependsOn, nil, "depends-on relationship targets, comma-separated and repeatable (containers require a plan- reference)")
	cmd.Flags().StringSlice(flagNewDerivesFrom, nil, "derives-from relationship targets, comma-separated and repeatable")
	cmd.Flags().StringSlice(flagNewValidates, nil, "validates relationship targets, comma-separated and repeatable")
	cmd.Flags().StringSlice(flagNewSupersedes, nil, "supersedes relationship targets, comma-separated and repeatable")
	cmd.Flags().StringSlice(flagNewAmends, nil, "amends relationship targets, comma-separated and repeatable")
	cmd.Flags().String(flagNewBatchFile, "", "scaffold a batch of drafts from a JSON file instead of a single target (batch schema documented above; exclusive with the single-target flags)")
	cmd.Flags().String(flagProject, "", "explicit project for workspace-native authoring: a project registered in the workspace (run 'eka project list'), targeted from any directory; requires --namespace or a qualified target")
	cmd.Flags().String(flagNewNamespace, "", "explicit namespace for workspace-native authoring: overrides the repository/target namespace (--project, when given, must be registered)")
	cmd.Flags().String(flagNewContentFile, "", "prepopulate the draft content from a JSON object file (agents); merged into the draft's content, raw text rejected")
	cmd.Flags().String(flagNewBy, "", "change-log authority name (default: `git config user.name`)")
	cmd.Flags().String(flagNewByKind, "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool(flagNewEdit, false, "TTY only: open $EDITOR (fallback vi) on the draft, then re-validate")
	return cmd
}

// newUsage renders a usage-class failure (exit 2) of `eka new`: the
// deterministic "eka: <message>" line on stderr (the same class the
// note/transition commands use for an unresolved --by source).
func newUsage(cmd *cobra.Command, message string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// resolveNewScope resolves the project and namespace of `eka new` per
// spec §3.2 + D6. Two paths:
//
//   - Repo context (no --project/--namespace): the project is the
//     repository's project, the namespace is the target's namespace or
//     (unqualified) the repository's default namespace. A qualified
//     target whose namespace differs from the repository's namespace
//     is refused — cross-platform access is read-only, so writing into
//     another platform's namespace is never allowed.
//   - Workspace-native (--project and/or --namespace given,
//     sto:workspace-native-authoring): the draft targets a project of
//     the workspace registry DIRECTLY from any directory. The explicit
//     path never borrows from the repository: an explicit --project
//     must be a REGISTERED project (the projects table of the
//     workspace; see projectRegistered) and an explicit --namespace
//     (or a qualified target) must supply the namespace — a bare
//     --project has no default namespace and is refused (ADR-017 D6
//     rejected --project as a repo override; the explicit pair is the
//     cross-project slot D6 kept open). --namespace alone resolves the
//     project from the repository context.
//
// The repository context gate (ADR-018): an EKA repository is a
// directory tree carrying eka.yaml — when the walk-up from the current
// directory finds no eka.yaml the tree is not an EKA repository and
// the repo-context path refuses deterministically (run 'eka init'
// first). The explicit path needs no repository at all. A metadata
// repository that is not registered yet (or whose namespace was never
// resolved) keeps the spec's hints.
func resolveNewScope(r *runtime.Runtime, ref conformance.Reference, projectFlag, nsFlag string) (project, ns string, err error) {
	abs, aerr := filepath.Abs(".")
	if aerr != nil {
		return "", "", fmt.Errorf("cannot resolve the current directory: %w", aerr)
	}
	abs = filepath.Clean(abs)
	repo, found, ferr := r.Workspace.FindRepo(abs)
	if ferr != nil {
		return "", "", ferr
	}

	// Workspace-native authoring: the explicit pair targets a
	// registered project from any directory (the ADR-018 gate and the
	// D6 cross-namespace refusal are repo-context rules and do not
	// apply to a deliberate explicit target).
	if projectFlag != "" || nsFlag != "" {
		return resolveExplicitScope(r, projectFlag, nsFlag, ref.Namespace, repo, found)
	}

	ns = ref.Namespace
	if !found {
		// The repository context gate (ADR-018): without eka.yaml the
		// tree is not an EKA repository — deterministic refusal, never
		// the legacy hints.
		_, _, hasMeta, merr := metadata.Find(abs)
		if merr != nil {
			return "", "", merr
		}
		if !hasMeta {
			return "", "", fmt.Errorf("refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
		}
	}
	if found {
		project = repo.ProjectID
		if ns == "" {
			ns = repo.Namespace
		}
		// D6: writing with a namespace that differs from the
		// repository's default is a refusal — cross-platform access is
		// read-only (qualified targets in eka get/eka export).
		if repo.Namespace != "" && ref.Namespace != "" && ref.Namespace != repo.Namespace {
			return "", "", fmt.Errorf("refused: namespace %s differs from the repository namespace %s; cross-platform access is read-only (qualified target <ns>/<type>:<id>)",
				ref.Namespace, repo.Namespace)
		}
	}
	if ns == "" {
		return "", "", fmt.Errorf("cannot resolve a namespace here; run inside a registered repository or run 'eka sync' once to resolve the repository identity from eka.yaml")
	}
	if project == "" {
		return "", "", fmt.Errorf("cannot resolve a project here; run inside a registered repository")
	}
	return project, ns, nil
}

// resolveExplicitScope resolves the project and namespace of the
// workspace-native authoring path (--project/--namespace given), shared
// by the single-target and the batch forms of `eka new`:
//
//   - --project P: P must be a project REGISTERED in the workspace (the
//     projects table — the authoritative project list; authoring
//     against an unknown project would scaffold drafts that cannot
//     publish coherently), and the namespace must come from --namespace
//     or the target's qualified namespace (refNS) — an explicit project
//     has no default namespace outside its repository context, so a
//     bare --project is a deterministic refusal, never a silent borrow
//     from eka.yaml.
//   - --namespace alone: the project resolves from the repository
//     registered at the current directory (the same context the default
//     path uses); outside a repository it is refused. --namespace alone
//     also overrides the repository namespace deliberately — D6 guards
//     only the implicit path.
//   - the resolved namespace must be a valid EKA identifier (the
//     eka.yaml identifier rule) — the explicit path validates it at
//     scaffold time so the draft is publishable.
func resolveExplicitScope(r *runtime.Runtime, projectFlag, nsFlag, refNS string, repo runtime.Repo, repoFound bool) (project, ns string, err error) {
	if projectFlag != "" {
		project = projectFlag
		registered, rerr := projectRegistered(r, project)
		if rerr != nil {
			return "", "", rerr
		}
		if !registered {
			return "", "", fmt.Errorf("refused: project %s is not registered in the workspace; run 'eka project list' to see registered projects, or register it with 'eka sync' / 'eka project register'", project)
		}
		if nsFlag == "" && refNS == "" {
			return "", "", fmt.Errorf("refused: --project %s requires --namespace (an explicit project has no default namespace); pass --namespace <ns> to author workspace-natively", project)
		}
	} else {
		// --namespace alone: the project still comes from the repository
		// context (there is no registry lookup by namespace).
		if !repoFound {
			return "", "", fmt.Errorf("cannot resolve a project here; run inside a registered repository or pass --project <name>")
		}
		project = repo.ProjectID
	}
	ns = nsFlag
	if ns == "" {
		ns = refNS
	}
	if !metadata.ValidIdent(ns) {
		return "", "", fmt.Errorf("refused: namespace %q is not a valid EKA identifier (lowercase letters, digits and single hyphens)", ns)
	}
	return project, ns, nil
}

// projectRegistered reports whether the project is registered in the
// workspace registry (the projects table — the authoritative project
// list of the workspace, populated by RegisterRepo/RegisterRepoMetadata
// and auto-registration at sync).
func projectRegistered(r *runtime.Runtime, project string) (bool, error) {
	projects, err := r.Workspace.Projects()
	if err != nil {
		return false, fmt.Errorf("cannot read the workspace project registry: %w", err)
	}
	for _, p := range projects {
		if p.ID == project {
			return true, nil
		}
	}
	return false, nil
}

// collectRelationships gathers the relationship targets of the five
// --<field> flags into exchange.Relationships, in canonical field
// order, each target stored verbatim (publish-time validation applies).
// The flags are StringSlice: repeated occurrences and comma-joined
// values both accumulate, so no target is silently dropped.
func collectRelationships(cmd *cobra.Command) []exchange.Relationship {
	var out []exchange.Relationship
	for _, f := range []struct{ flag, rel string }{
		{flagNewDependsOn, "depends-on"},
		{flagNewDerivesFrom, "derives-from"},
		{flagNewValidates, "validates"},
		{flagNewSupersedes, "supersedes"},
		{flagNewAmends, "amends"},
	} {
		values, _ := cmd.Flags().GetStringSlice(f.flag)
		for _, part := range values {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out = append(out, exchange.Relationship{Type: f.rel, Target: part})
		}
	}
	return out
}

// --- editor + draft re-validation --------------------------------------

// runEditor opens $EDITOR (fallback vi) on the draft file, inheriting
// the process's stdio.
func runEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// printDraftValidationError renders a CKO-level draft validation error
// after an editor session: structural findings (*conformance.ScanError)
// are shown in the report format; other errors (missing draft, internal)
// are rendered as a deterministic verdict line. Never destructive.
func printDraftValidationError(s *ui.Style, target string, err error) {
	fmt.Fprintf(s.W, "\n%s\n", s.Accent("Draft validation"))
	var se *conformance.ScanError
	if errors.As(err, &se) {
		fmt.Fprintf(s.W, "Verdict: %s\n", s.Error("FAIL"))
		for _, res := range se.Findings {
			fmt.Fprintf(s.W, "  [%s] %s: %s\n", res.Severity, res.Rule, res.Message)
		}
		return
	}
	fmt.Fprintf(s.W, "Verdict: %s (%v)\n", s.Error("FAIL"), err)
}

// --- eka edit ----------------------------------------------------------

// newEditCommand builds `eka edit <target>`: open an existing draft in
// $EDITOR (fallback vi). Strictly draft-only and TTY-only: published
// canonical forms are refused (immutable CKOs have no edit path) and
// non-interactive runs use --content-file at `eka new` or inline
// publishing instead. After the editor closes, the draft is
// re-validated at CKO level (the same validation publish runs) and the
// findings are shown — blocking violations are reported, never
// destructive: fix them in the next edit and publish.
func newEditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <target>",
		Short: "Open a draft in the editor",
		Long: `Open an existing draft in $EDITOR (fallback vi) for authoring.

The target is a draft identity: <type>:<id> or <ns>/<type>:<id>.
Published canonical forms (<ns>/<type>:<id>:<v>) are refused: a
published knowledge object is immutable and has no edit path.

The command requires an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — outside an EKA repository
the command is refused).

TTY only: outside a terminal the command is refused — agents author
through 'eka new --content-file' or inline publishing.

After the editor closes, the draft is re-validated at CKO level (the
same validation publish runs, with the workspace as the reference
universe) and the verdict is shown. Blocking violations are reported,
never destructive: fix them in the next edit and publish.

Exit codes:
  0  edited and re-validated (findings, if any, are reported)
  2  refused (non-TTY, published form, draft not found, not an EKA
     repository) or internal`,
		Example: `  eka edit feather/sto:my-item`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := parseDraftTarget(args[0]); err != nil {
				return fmt.Errorf("edit: %w", err)
			}
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the command is refused
			// (exit 2, edit's usage/internal error class).
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("edit: %w", err)
			}
			abs = filepath.Clean(abs)
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("edit: %w", err)
			}
			if !hasMeta {
				return fmt.Errorf("edit refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
			}
			if !isTTYReader(cmd.InOrStdin()) {
				return fmt.Errorf("edit: requires a terminal; agents use --content-file at 'eka new' or inline publish")
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			project, _ := cmd.Flags().GetString(flagProject)
			// The cross-project fallback resolves the draft wherever it
			// lives (the same resolution publish and discard use).
			df, err := runtime.Authoring.ResolveDraft(r, args[0], project)
			if err != nil {
				var dne *runtime.DraftNotFoundError
				if errors.As(err, &dne) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: edit: %s\n", err)
					return &exitError{code: exitUsage}
				}
				return fmt.Errorf("edit: %w", err)
			}
			if err := runEditor(df.Path); err != nil {
				return fmt.Errorf("edit: editor failed: %w", err)
			}
			// CKO-level re-validation (spec §4.2): the same validation
			// publish runs, non-destructive — the draft stays and the
			// findings are shown for the next edit.
			s := styleFor(cmd)
			dv, err := runtime.Authoring.ValidateDraft(r, args[0], project)
			if err != nil {
				printDraftValidationError(s, args[0], err)
			} else {
				if dv.Ref.Note != "" {
					fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info(dv.Ref.Note))
				}
				printCKOReport(s, args[0], dv.Report)
			}
			return nil
		},
	}
	cmd.Flags().String(flagProject, "", "project scope (default: the project owning the current repository)")
	return cmd
}

// --- eka draft list ----------------------------------------------------

// newDraftCommand builds the `eka draft` command tree.
func newDraftCommand() *cobra.Command {
	draft := &cobra.Command{
		Use:   "draft",
		Short: "Manage drafts",
		Long: `Manage the drafts of the authoring workflow: drafts are mutable
workspace-local authoring files (<workspace>/drafts/...) that become
immutable knowledge objects at publish time.

Subcommands:
  list      render the draft backlog with per-draft validation markers
  validate  validate one draft at CKO level without publishing it`,
	}
	draft.AddCommand(newDraftListCommand())
	draft.AddCommand(newDraftValidateCommand())
	return draft
}

// newDraftListCommand builds `eka draft list [--project <name>]`: the
// deterministic draft backlog — project (by name), then type, then id —
// with the per-draft validation marker. Informational: exit 0 even
// when the backlog is empty.
func newDraftListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the draft backlog",
		Long: `List the draft backlog of the EKA workspace in the themed CLI style
(icons + color on a TTY, plain text otherwise): each draft row shows
the type:id identity, its namespace, the last-modified time and, when
the draft fails the single-file structural classification, an
"invalid — N errors" marker (a draft is only truly validated at
publish time).

Ordering is deterministic: project (by name), then type, then id.

Exit codes:
  0  informational (also when the backlog is empty)
  2  internal error`,
		Example: `  eka draft list
  eka draft list --project atrium`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString(flagDraftListProject)
			s := styleFor(cmd)
			// Informational command: never initializes the workspace.
			// A missing workspace is simply an empty backlog.
			r, err := runtime.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			var drafts []runtime.Draft
			if r.Exists() {
				drafts, err = runtime.Authoring.Drafts(r, project)
				if err != nil {
					return err
				}
			}
			renderDraftList(s, r, drafts, project)
			return nil
		},
	}
	cmd.Flags().String(flagDraftListProject, "", "project scope (default: all projects)")
	return cmd
}

// renderDraftList renders the draft backlog deterministically: project
// group headers (•), then per-draft rows (• type:id (namespace)
// updated <mtime> [invalid marker]), then the summary counts.
func renderDraftList(s *ui.Style, r *runtime.Runtime, drafts []runtime.Draft, project string) {
	fmt.Fprintln(s.W, s.Accent("Drafts"))
	scope := project
	if scope == "" {
		scope = "all"
	}
	fmt.Fprintf(s.W, "Project   %s\n", scope)
	projects := 0
	cur := ""
	for _, d := range drafts {
		if d.Project != cur {
			cur = d.Project
			projects++
			fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, cur)
		}
		ns := d.Namespace
		if ns == "" {
			ns = "?"
		}
		marker := draftValidationMarker(r, s, d)
		fmt.Fprintf(s.W, "  %s %s:%s     (%s)     updated %s%s\n",
			ui.IconBullet, d.Type, d.ID, ns, d.Updated, marker)
	}
	ui.NewSummary(s).
		Add("Drafts", fmt.Sprintf("%d (%d projects)", len(drafts), projects)).
		Render()
}

// draftValidationMarker renders the per-draft validation marker (spec
// §4.3): the draft is validated at CKO level exactly like publish would
// (runtime.Authoring.ValidateDraft — the shared M3/M4 helper), and a
// draft failing the validation is marked "invalid — N errors". Warnings
// never mark a draft.
func draftValidationMarker(r *runtime.Runtime, s *ui.Style, d runtime.Draft) string {
	dv, err := runtime.Authoring.ValidateDraft(r, d.Type+":"+d.ID, d.Project)
	if err != nil {
		var se *conformance.ScanError
		if errors.As(err, &se) {
			return "    " + s.Warning(fmt.Sprintf("invalid — %s", plural(len(se.Findings), "error", "errors")))
		}
		return "    " + s.Warning("invalid — unreadable")
	}
	if !dv.Report.Pass() {
		return "    " + s.Warning(fmt.Sprintf("invalid — %s", plural(dv.Report.ErrorCount(), "error", "errors")))
	}
	return ""
}

// --- eka draft validate ------------------------------------------------

// newDraftValidateCommand builds `eka draft validate <target>`: run the
// CKO-level validation publish runs, without persisting anything. The
// draft stays untouched — the early-warning loop for the publish gate
// (the same shared pipeline `eka edit` re-validation and the `eka draft
// list` marker use, non-destructive by contract).
func newDraftValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <target>",
		Short: "Validate a draft without publishing",
		Long: `Validate one draft at CKO level without publishing it: the same
validation publish runs, non-destructive — the draft file is never
written, removed or inserted. The command is the early-warning loop
for the publish gate: a draft that validates as-is can be published;
a draft that fails validation renders the same report 'eka publish'
would refuse with, before the draft is ever consumed.

The target is a draft identity: <type>:<id> or <ns>/<type>:<id>. The
project is the repository registered at the current directory (a
draft outside it is resolved through the cross-project fallback,
like publish's); a target namespace, when present, must equal the
draft's frontmatter namespace.

Exit codes:
  0  draft valid (warnings allowed)
  1  validation findings, malformed draft, or draft not found
  2  usage or internal error`,
		Example: `  eka draft validate feather/sto:my-item`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if _, err := parseDraftTarget(target); err != nil {
				return fmt.Errorf("validate: %w", err) // Exit 2: usage.
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			dv, err := runtime.Authoring.ValidateDraft(r, target, "")
			if err != nil {
				var se *conformance.ScanError
				if errors.As(err, &se) {
					printScanReport(styleFor(cmd), target, se)
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				var dne *runtime.DraftNotFoundError
				if errors.As(err, &dne) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				return err // Exit 2: internal.
			}

			s := styleFor(cmd)
			if dv.Report.Pass() {
				fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success(fmt.Sprintf(
					"%s is valid (%d %s, %d %s) — ready to publish",
					target,
					dv.Report.ErrorCount(), plural(dv.Report.ErrorCount(), "error", "errors"),
					dv.Report.WarningCount(), plural(dv.Report.WarningCount(), "warning", "warnings"))))
				return nil
			}
			printCKOReport(s, target, dv.Report)
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s failed validation\n", target)
			return &exitError{code: exitFail}
		},
	}
	return cmd
}

// --- eka publish -------------------------------------------------------

// newPublishCommand builds `eka publish <target> [--instance-version
// v]`: validate one draft at CKO level and persist it as an immutable
// Canonical Knowledge Object, then remove the draft.
//
// Documented deviation from spec §4.4: the spec's --sync flag is not
// implemented. Published objects are workspace-native (provenance
// source_repo = "runtime") and have no repository to push — 'eka sync'
// remains the explicit transport step for repository-attributed
// knowledge.
func newPublishCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish <target> | --all | --pending",
		Short: "Publish a draft (or all pending drafts) as immutable knowledge objects",
		Long: `Validate one draft at CKO level and persist it as an immutable
Canonical Knowledge Object in the workspace database, then remove the
draft file (all-or-nothing: a failed validation or insert keeps the
draft untouched; the draft file is the single-use ticket, so a second
publish of the same draft fails at the read).

The target is a draft identity: <type>:<id> or <ns>/<type>:<id>. The
project is the repository registered at the current directory; a
target namespace, when present, must equal the draft's frontmatter
namespace.

Batch publishing (--all | --pending — the two flags are synonyms):
publish EVERY pending draft of the repository's project in topological
order: a draft is published only after every draft it references
(depends-on/derives-from/validates/supersedes/amends/discusses/
replies-to targets that are pending drafts) is published, so the
published set satisfies the resolution rules (rule 8's tkt -> ctr
chain among them). Ties are broken by identity, so the order is
deterministic for a given backlog.

The batch run refuses BEFORE publishing anything when:
  - the pending draft graph contains a cycle (the refusal names the
    drafts that cannot be ordered; a self-reference is a cycle);
  - a draft references a target that is neither a pending draft nor an
    object already in the canonical store (the refusal names the draft
    and the target) — stricter than rule 5's draft tolerance, because
    the published set must be coherent.

The publish loop is per-draft atomic: a draft failing CKO-level
validation stops the run — the objects already published stay
(immutability), the failing draft and everything ordered after it stay
pending, and the failure report names them. An empty backlog is
informational: nothing to publish, exit 0.

The command requires an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — outside an EKA repository
the command is refused).

The instance version is auto-assigned as the line's highest + 1
(1 for a new line), honoring an instance-version in the draft
frontmatter when present; --instance-version overrides both and must
exceed the line's highest (forward-only). --instance-version is a
single-target flag: with --all/--pending versions are auto-assigned
per draft, and the combination is a usage error instead of a silent
drop.

Publish never auto-syncs: published objects are workspace-native and
have no repository to push. 'eka sync' remains the explicit transport
step for repository-attributed knowledge.

Exit codes:
  0  published (form + instance version + object hash); --all with an
     empty backlog (informational)
  1  validation failure, malformed draft, draft not found, batch cycle
     or unresolved-reference refusal, or not an EKA repository
  2  usage or internal error`,
		Example: `  eka publish feather/sto:my-item
  eka publish feather/sto:my-item --instance-version 2
  eka publish --all
  eka publish --pending`,
		Args: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool(flagPublishAll)
			pending, _ := cmd.Flags().GetBool(flagPublishPending)
			if all || pending {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Batch publishing: --all / --pending (synonyms) publish
			// every pending draft of the project in topological order.
			// --instance-version is single-target only (versions are
			// auto-assigned per draft in the batch) and is refused
			// instead of silently dropped.
			if all, _ := cmd.Flags().GetBool(flagPublishAll); all {
				if cmd.Flags().Changed(flagPublishVersion) {
					return fmt.Errorf("publish: --instance-version is a single-target flag and is not available with --all/--pending; versions are auto-assigned per draft")
				}
				return runPublishBatch(cmd)
			}
			if pending, _ := cmd.Flags().GetBool(flagPublishPending); pending {
				if cmd.Flags().Changed(flagPublishVersion) {
					return fmt.Errorf("publish: --instance-version is a single-target flag and is not available with --all/--pending; versions are auto-assigned per draft")
				}
				return runPublishBatch(cmd)
			}
			target := args[0]
			if _, err := parseDraftTarget(target); err != nil {
				return fmt.Errorf("publish: %w", err) // Exit 2: usage.
			}
			version, err := cmd.Flags().GetInt(flagPublishVersion)
			if err != nil {
				return fmt.Errorf("publish: %w", err)
			}
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the command is refused
			// in publish's refusal style (exit 1).
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("publish: %w", err)
			}
			abs = filepath.Clean(abs)
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("publish: %w", err)
			}
			if !hasMeta {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: publish refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first\n", abs)
				return &exitError{code: exitFail}
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			// The project resolves from the repository registered at
			// the current directory (the --project flag was removed,
			// ADR-017 D6); an empty project triggers the cross-project
			// draft fallback inside the Authoring API.
			res, err := runtime.Authoring.Publish(r, target, runtime.PublishOptions{
				InstanceVersion: version,
			})
			if err != nil {
				var pe *runtime.PublishError
				if errors.As(err, &pe) {
					printCKOReport(styleFor(cmd), target, pe.Report)
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				var se *conformance.ScanError
				if errors.As(err, &se) {
					printScanReport(styleFor(cmd), target, se)
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				var dne *runtime.DraftNotFoundError
				if errors.As(err, &dne) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				return err // Exit 2: internal.
			}

			s := styleFor(cmd)
			ui.NewHeader(s, "Draft").
				Add("Target", target).
				Pipeline("Publish").
				Render()
			ui.NewSummary(s).
				Add("Published", res.Form).
				Add("Instance Version", fmt.Sprint(res.InstanceVersion)).
				Add("Object Hash", res.ObjectHash).
				Add("Next", "eka get "+res.Form).
				Render()
			// A converged concurrent publish (spec §5.2 single-writer
			// race): the object was persisted by this run and the draft
			// file was already gone — reported, not an error.
			if res.Note != "" {
				fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info(res.Note))
			}
			return nil
		},
	}
	cmd.Flags().Int(flagPublishVersion, 0, "explicit instance version (must exceed the line's highest; default: auto-assign; not available with --all/--pending)")
	cmd.Flags().Bool(flagPublishAll, false, "publish every pending draft of the project in topological order (referenced drafts first)")
	cmd.Flags().Bool(flagPublishPending, false, "synonym of --all: publish every pending draft of the project in topological order")
	return cmd
}

// printCKOReport renders a CKO-level validation report (the findings of
// a refused publish or a post-edit re-validation) in the deterministic
// report format.
func printCKOReport(s *ui.Style, target string, r *conformance.Report) {
	fmt.Fprintf(s.W, "%s\n", s.Accent("Draft validation"))
	fmt.Fprintf(s.W, "%s\n", s.Dim(fmt.Sprintf("Draft: %s — %d errors, %d warnings",
		target, r.ErrorCount(), r.WarningCount())))
	fmt.Fprintf(s.W, "\nResults (sorted by rule):\n")
	results := r.SortedResults()
	if len(results) == 0 {
		fmt.Fprintf(s.W, "  (no violations found)\n")
	} else {
		for _, res := range results {
			fmt.Fprintf(s.W, "  [%s] %s %s: %s\n", res.Severity, res.Rule, res.File, res.Message)
		}
	}
	verdict := "PASS"
	if !r.Pass() {
		verdict = "FAIL"
	}
	if r.Pass() {
		fmt.Fprintf(s.W, "\nVerdict: %s\n", s.Success(verdict))
	} else {
		fmt.Fprintf(s.W, "\nVerdict: %s\n", s.Error(verdict))
	}
}

// printScanReport renders the structural findings of a malformed draft.
func printScanReport(s *ui.Style, target string, se *conformance.ScanError) {
	fmt.Fprintf(s.W, "%s\n", s.Accent("Draft validation"))
	fmt.Fprintf(s.W, "%s\n", s.Dim(fmt.Sprintf("Draft: %s — %d structural errors",
		target, len(se.Findings))))
	fmt.Fprintf(s.W, "\nResults (sorted by rule):\n")
	for _, res := range se.Findings {
		fmt.Fprintf(s.W, "  [%s] %s: %s\n", res.Severity, res.Rule, res.Message)
	}
	fmt.Fprintf(s.W, "\nVerdict: %s\n", s.Error("FAIL"))
}

// --- eka discard -------------------------------------------------------

// newDiscardCommand builds `eka discard <target> [--force]
// [--project <name>]`: delete a draft without publishing. A TTY prompt
// confirms unless --force; outside a terminal --force is required.
func newDiscardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discard <target>",
		Short: "Discard a draft without publishing",
		Long: `Delete a draft without publishing it. The draft is gone for good —
there is no undo (the published path is 'eka publish').

The target is a draft identity: <type>:<id> or <ns>/<type>:<id>. The
project is --project, else the repository registered at the current
directory.

The command requires an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — outside an EKA repository
the command is refused).

On a terminal the command prompts for confirmation; outside a
terminal --force is required (agents decide programmatically).

Exit codes:
  0  discarded (or declined at the prompt)
  2  draft not found, published form, not an EKA repository, or
     usage/internal`,
		Example: `  eka discard feather/sto:my-item
  eka discard feather/sto:my-item --force
  eka discard feather/sto:my-item --force --project atrium`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if _, err := parseDraftTarget(target); err != nil {
				return fmt.Errorf("discard: %w", err) // Exit 2: usage.
			}
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the command is refused
			// (exit 2, discard's usage/internal error class).
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("discard: %w", err)
			}
			abs = filepath.Clean(abs)
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("discard: %w", err)
			}
			if !hasMeta {
				return fmt.Errorf("discard refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
			}
			force, _ := cmd.Flags().GetBool(flagDiscardForce)
			project, _ := cmd.Flags().GetString(flagProject)
			if !force {
				if !isTTYReader(cmd.InOrStdin()) {
					return fmt.Errorf("discard: requires --force when not running in a terminal")
				}
				confirmed, err := confirmDiscard(cmd, target)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintf(cmd.OutOrStdout(), "Discarded nothing; draft %s kept.\n", target)
					return nil
				}
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()
			note, err := runtime.Authoring.DiscardDraft(r, target, project, force)
			if err != nil {
				var dne *runtime.DraftNotFoundError
				if errors.As(err, &dne) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitUsage}
				}
				return err
			}
			if note != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", note)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Discarded draft %s.\n", target)
			return nil
		},
	}
	cmd.Flags().Bool(flagDiscardForce, false, "discard without the confirmation prompt")
	cmd.Flags().String(flagProject, "", "project scope (default: the project owning the current repository)")
	return cmd
}

// confirmDiscard prompts on stdout and reads the confirmation from
// stdin; y/yes confirm, anything else declines.
func confirmDiscard(cmd *cobra.Command, target string) (bool, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "Discard draft %s? [y/N] ", target)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("discard: cannot read the confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
