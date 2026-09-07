---
name: eka-shr-non-eka
description: Non-EKA deep audit shr skill — audit filesystem codebase for non-EKA sharing, level-adjusted deep scan
---
# EKA Shr Non-EKA

Audit a non-EKA codebase (filesystem path) into shr with level-adjusted deep audit.

- L0: shallow file list only (fast)
- L1/L2: deep scan docs + codegraph + sensitivity redaction (README, manifests, codegraph, secrets filtered)

Usage:
- `eka shr build /path/to/codebase --provenance audited --level L1`
- `--provenance audited` triggers auditNonEKAPathLevel (deep audit)
- Output usable for snapshot validation after redaction
