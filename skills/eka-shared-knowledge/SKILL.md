---
name: eka-shared-knowledge
description: Use when developing a project and you need context from ANOTHER project or codebase that was shared as shr knowledge — discover, inspect and consume shared snapshots (overview, structure, API contracts) without cloning the source repo.
---

# Consuming Shared Knowledge

Shared knowledge (`shr`) is point-in-time snapshots of other projects, stored in the workspace (`~/.eka`) — readable from any directory, no `eka.yaml` required. Use it instead of cloning the source repo.

## The workflow (run exactly)

```bash
# 1. Discover — clean list, full ns/type:id
eka shr list
eka shr list --level L0 --project <name> --json   # filtered, machine

# 2. Select — pick by project + version + level (see table below)

# 3. Consume — one share at the depth you need
eka shr show <ns/type:id>                    # human detail
eka shr show <ns/type:id> --json             # full payload (agent)
eka shr show <ns/type:id> --with-docs        # L2 deepDocs when present

# 4. Apply — cite the pin in your work
# reference: <ns/type:id>:<v> @ sourceHash <hash> (version <ver>)
```

Inside an EKA repository the same data is reachable via `eka get operations --type shr` / `eka view operations --type shr`; via MCP use the `get`/`domain` tools with `level`/`project`/`version`.

## Level selection

| You need | Level | Where it lives |
|---|---|---|
| what the project is, one-paragraph overview | L0 | `description` |
| structure: stack, domains, entry points | L1 | `summary` |
| full content / API contracts / business logic | L2 | `snapshot` (EKA) — see `references/consumption-levels.md` |

Start at L0; go deeper only when the task needs it (token economy).

## Trust & freshness

| Field | Meaning |
|---|---|
| `provenance: extracted` | from the source's live KMS — authoritative |
| `provenance: audited` | filesystem scan — point-in-time, may be shallow |
| `sourceHash` | pin of the source at build time |
| `sourceVersion` | source project semver (major immutable) |

Before building on a share, decide if staleness matters: re-run `eka shr build` on the source (see `eka-shr-builder`) and compare `sourceHash`, or treat the share as advisory.

## Guardrails

- Do not clone or read the source code when an L2 `snapshot` already answers the question.
- A share is a snapshot, not a live query — never present it as the current state of the source.
- Shares are workspace-local by default: a clone on another device receives none. For cross-device delivery see `references/durability.md`.
- Deleting is permanent and bulk filters cross projects — prefer `eka shr delete <ns/type:id>` (single, explicit).
