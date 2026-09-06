# SES Execution State Model

- **Token:** `ses:execution-state` (Execution, stratum 4, ExistenceState only)
- **Content keys:** `scope`, `current`, `next`, `items`, `decisions`, `mode`, plus base `context`, `notes`, `verification` (R9 required)
  - `scope`: selector string, e.g. `ctr:status-execution-state`
  - `current`: object `{item, phase, worktree, pending}` — the atomic unit currently in-flight
  - `next`: string line form of next item
  - `items`: array of `{id, state}` for container items
  - `decisions`: array of strings
  - `mode`: `solo|delegated`
- **Immutability:** every update publishes new `instanceVersion` (append-only), previous versions retrievable via `eka get eka/ses:execution-state:<v>` and `timeline`
- **Attribution:** published via `eka publish` from repo context, snapshot push transports via `source_repo` provenance pair (project_id, source_repo), clone receives full history
- **Retrieval:** `eka get eka/ses:execution-state` (latest), `eka get eka/ses:execution-state:1`, timeline via store Timeline service

This file documents ADR 036/037 ses model as implemented in eka-cli v1.11+.
scripts/ses-init-migrate.sh idempotent init migration
