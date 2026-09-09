---
name: eka-shr-builder
description: Builds shr snapshots (L0-L2, pinned sourceHash) for any EKA project using eka as KMS (any repo with eka.yaml) and audited filesystem codebases — use when sharing knowledge without cloning source or publishing L0-L2 via CLI/MCP.
---

# Building shr snapshots (EKA-to-EKA = any project with eka.yaml)

## When to use
- **EKA** (any project with `eka.yaml`, not just `project eka`): source is qualified CKO (`<ns>/<type>:<id>`) — `eka get <source>` succeeds → extract from live KMS (no FS scan), except **L2 deep scan** for API contracts
- **Non-EKA** (no `eka.yaml`): source is directory path → `eka-shr-non-eka` (L0 shallow vs L1/L2 deep scan)

## Run exactly (low-freedom)
```bash
eka shr build <source> --level L0 --id <shr-id>
eka shr build <source> --levels L0,L1,L2
eka publish <ns>/shr:<id>
eka shr build <source> --level L0 --adopt   # publish + adopt into this repo's snapshot + push (clones receive it)
```

## Flags
| Flag | Values | Note |
|---|---|---|
| `--level` | `L0\|L1\|L2` required | `L0` meta, `L1` +summary, `L2` +snapshot + **deepDocs** (EKA L2 scans `docs/spec/api`) |
| `--levels` | `L0,L1,L2` batch | 1–3 shr, suffix `-l0/-l1/-l2` |
| `--provenance` | `extracted` (EKA), `audited` (non-EKA path) | auto-hint if path has `eka.yaml` → should be CKO |
| `--project/--version` | `sourceProject`/`sourceVersion` | from `eka.yaml` (any EKA project) via `resolveNewScope`; semver: 3-part major immutable, 2-part minor |

## Scan without source (MCP parity)
```bash
eka get operations --type shr --level L0 --project my-app --version 1.2.3
eka get <ns>/shr:<id> --level L0 --project X --version Y
```

See `references/shr-levels.md` for payload, `references/semver-immutability.md` for version, `references/eka-l2-deep.md` for EKA L2 deepDocs.

## EKA vs non-EKA (clarified)
- **EKA** = any repo with `eka.yaml` (KMS live) → `extract` from KMS (no FS scan) except L2 deepDocs
- **Non-EKA** = no `eka.yaml` → ask `project` id, `audited` spike with level-adjusted scan

For non-EKA detail see `eka-shr-non-eka`.

## List & show (global, outside repo)

```bash
eka shr list --level L0 --project X --version Y --json      # clean id+level, global
eka shr show <id> --level L0 --project X --json --with-docs # strict filter, L2 deepDocs
# eka get/view operations --type shr --level L0 --project X  # inside repo, clean list id+level (view parity)
```

`shr list` default clean `id + level`; `--verbose` adds project/version/title, `--json` machine. `view operations` now supports `--type/--level/--project/--version`.

