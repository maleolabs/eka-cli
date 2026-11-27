# Upgrading to EKA 2.0

EKA 2.0 removes the **work-ownership model** by *deprecating in place*: nothing is
hard-deleted, existing knowledge stays readable, and attribution is untouched.

## What changed

- **Ownership removed, not erased.** The `mbr-` token and the `assigned-to`
  relationship are now **read-only**. No new `mbr-` line and no new `assigned-to`
  edge can be authored (`eka new`, `eka relate`, the MCP `new`/`relate` tools, and
  `eka assign`/`reassign`/`unassign` all refuse them). Existing instances remain
  recognized, resolvable and exported unchanged. Removal is deferred one major
  (3.0).
- **Commands removed.** `eka assign`, `eka reassign`, `eka unassign` are gone.
- **Member-scoped projections removed.** The member board, `eka view --member`,
  and the `assignee` / `no-assignee` machine keys are gone. The ordinary
  `eka view` / `eka board` (container-tag board) is unchanged.
- **MCP surface shrunk.** The `workspace` capability group exposes only
  `sync_push`; `assign`/`reassign`/`unassign` are removed from schemas, dispatch
  and the approval policy. The server advertises **six grouped handles**.
- **Skill pack scrubbed.** No `RoleVocabulary`/`ModeDelegate`, no `mappings/*.toml`,
  no `DELEGATION.txt`, no `mbr-template.json`; `/eka-discuss` and `/eka-execute`
  run on `mode: solo`.
- **Attribution unchanged.** `author` / `by` / `by-kind` (including `worker`) are
  untouched.

## Compatibility

- **No forced migration.** A v2 client reads a v1 store and a v1 snapshot
  unchanged: `SerializationVersion` stays `2`; the exchange specification now
  accepts `2.0` **and** the legacy `1.7`/`1.1` versions. Knowledge you authored
  before the upgrade keeps resolving.
- **Mixed versions.** A v1 client may still write `assigned-to` edges; a v2 client
  reads them fine (read tolerance). Nothing breaks — the edges are simply inert.
- **Module path.** Every module moved to a `/v2` path
  (`github.com/maleolabs/eka-core/v2`, `.../eka-cli/v2`, `.../eka-mcp/v2`) so the
  `v2.0.0` major tag is valid under Go semantic import versioning. A Go consumer
  must update its `require` lines; the API surface is otherwise unchanged.
- **Consumer contract.** A consumer drives the runtime through
  `github.com/maleolabs/eka-core/v2/api`; importing the internal packages
  (`runtime/`, `store/`, `workspace/`, `sync/`) is deprecated and refused by an
  architecture test in `eka-mcp` v2.0.0. The `eka-cli` migration to `api` is
  tracked separately; it remains a direct importer through the v2 line.

## Upgrade steps

1. Upgrade the CLI / MCP / skill pack to the 2.0 line.
2. Nothing to migrate: your knowledge is read as-is.
3. (Optional, forward-only) Tidy legacy ownership out of your store: retire the
   `mbr-` lines with `eka retire` and drop `assigned-to` edges. This is cosmetic
   and reversible at the line level; it is **not** required for operation.

> A one-shot `eka migrate v2 --dry-run` convenience command (bulk-retire `mbr-`
> lines, unlink `assigned-to` edges, deterministic report) is planned for the v2
> line (WI `eka/sto:migrate-v2-command`). It is deliberately **not** part of the
> 2.0.0 cut: read tolerance means it is never a prerequisite, and the release
> ships the removal with no migration step.
