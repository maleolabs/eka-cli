---
name: eka-shr-non-eka
description: Audits non-EKA filesystem codebases (no eka.yaml) into shr via level-adjusted deep scan — use when source is directory, L0 shallow vs L1/L2 docs+codegraph+redaction.
---

# Auditing non-EKA (no eka.yaml)

## When to use
Source is directory path, `eka.yaml` missing → asks `project` id (not hardcode `eka`). For EKA (any project with `eka.yaml`), see `eka-shr-builder` (extract from KMS, L2 deepDocs).

## Run exactly
```bash
eka shr build /path/to/code --provenance audited --level L0 --id share-<base>-l0
eka shr build /path/to/code --provenance audited --levels L0,L1
eka publish <ns>/shr:<id>
```

## Levels
| Level | Scan | Cap |
|---|---|---|
| L0 | file list only | 500, skip .git/node_modules/.eka/dist |
| L1/L2 | +docs(.md)+codegraph(.go/.ts/.js/.py/.yaml) + redaction(.env/secret/.pem) | 1000, 1MiB guard, hash per level |

See `references/audit-levels.md`.

## Scan (MCP parity)
```bash
eka get operations --type shr --level L0 --project my-app
```
