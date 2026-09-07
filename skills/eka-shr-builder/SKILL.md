---
name: eka-shr-builder
description: EKA-to-EKA shr builder skill — build shr sharing objects from EKA CKO with per-project identifier, level opt-in, province extracted
---
# EKA Shr Builder

Build shr from an EKA source CKO. Uses eka.yaml project/namespace (not hardcode eka), captures sourceProject/sourceVersion with semver immutability.

- `eka shr build <source> --level L0|L1|L2` — opt-in, default share nothing
- `eka shr build <source> --levels L0,L1,L2` — batch 3 levels
- `eka get operations --type shr --level L0 --project my-app --version 1.2.3` — server-side filters
- Namespace derived from eka.yaml via resolveNewScope

Levels:
- L0 metadata only
- L1 + safe summary
- L2 + full snapshot (1MiB guard)

Skills are English, eka- prefix, discoverable via MCP `eka get` with Operations --level.
