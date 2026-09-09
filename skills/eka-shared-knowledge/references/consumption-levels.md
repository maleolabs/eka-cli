# Consumption levels — what each level carries

| Field | L0 | L1 | L2 |
|---|---|---|---|
| title, description (overview) | ✓ | ✓ | ✓ |
| level, provenance, sourceHash | ✓ | ✓ | ✓ |
| sourceProject / sourceVersion / sourceNamespace | ✓ | ✓ | ✓ |
| summary (structure: type, dimension, domain, content keys) | — | ✓ | ✓ |
| snapshot (full content at sourceHash) | — | — | ✓ |
| deepDocs (docs/spec/api of the build cwd) | — | — | EKA only, often absent |

Reading L2:
- **EKA source (`provenance: extracted`)** — `snapshot` IS the deep knowledge: the full CKO content (ADR decision, spec contract, requirement text). Read it; `deepDocs` is a bonus that exists only when the building cwd had `docs/spec/api` directories.
- **Non-EKA source (`provenance: audited`)** — `snapshot` is the audit summary (file inventory, docs and codegraph samples, secrets redacted). Treat it as orientation, not specification.

Machine paths:
- `eka shr show <ns/type:id> --json` — full payload
- `eka shr list --json` — discovery list (id, level, project, version, title, form)
- `eka get <ns>/shr:<id>` — canonical CKO document (schema eka-cko-v2)
- `eka context <ns>/shr:<id>` — the share's knowledge neighborhood (Stratum 5, Operations)
