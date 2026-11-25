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
- **Scoping:** each project publishes its own line under its own namespace from `eka.yaml` (`<ns>/ses:execution-state`, e.g. `nest-desacorp-mobile/ses:execution-state`). `eka status` searches project-local ses first (`Knowledge.Search` with the cwd repo's `ProjectID` + `Namespace`), falling back to the legacy global `eka/ses:execution-state` line only when no project-local line exists. Pre-schema-v3 repos (empty `Namespace`) resolve as `eka`.
- **Authoring:** `eka new <ns>/ses:execution-state --project <proj> --namespace <ns>`, edit `scope`/`current`/`next`/`items`/`mode`/`decisions` plus R9 base keys (`context`, `notes`, `verification`), then `eka publish <ns>/ses:execution-state`. `scripts/ses-sync.sh` derives `<proj>`/`<ns>` from the repo's `eka.yaml` (fallback `eka`); `scripts/ses-init-migrate.sh` is idempotent per project.
- **Retrieval:** `eka get <ns>/ses:execution-state` (latest), `eka get <ns>/ses:execution-state:1`, timeline via store Timeline service

This file documents ADR 036/037 ses model as implemented in eka-cli v1.11+.
scripts/ses-init-migrate.sh idempotent init migration
