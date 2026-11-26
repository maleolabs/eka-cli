# AGENTS

<!-- sto:skill-pack-tiered-routing-real — T0 frozen -->

<!-- eka:workflow:start -->
## EKA Workflow Context (auto-managed; edit outside the markers only)
Project: eka | Namespace: eka | Position: `eka status`
Knowledge lives in the canonical store, not repo files. Loop: Understand -> Context -> Reason -> Change -> Validate -> Publish.
Read: `eka status` · `eka get <ns>/<type>:<id>` · `eka context <subject> --depth engineering` · `eka view <projection>`.
Change: `eka new` -> `eka validate` -> `eka publish`; `eka transition <line> <to>`; evidence `eka note --role <role>`; owner `eka assign <item> --to <mbr>`.
Rules: never edit store data directly; transitions are gated and forward-only (read real state via `eka get`); after each transition run `eka sync push`.
Skills: `eka-router` first, then `eka-orientation` (mental model), `eka-engineering-workflow` (loop), `eka-troubleshooting` (refusals).
Constraints (Discovery + Architecture, stratum 1-2; index only — read one with `eka get <ns>/<type>:<id>`):
fnd: agent-agnostic-skill-pack eka-acronym-collision eka-product-identity-and-market-position full-codebase-review-2026-11 knowledge-sharing llm-context-efficiency rag-db-optimization revision-resets-object-state status-stale-instance-active team-collaboration
req: agent-agnostic-skill-pack agent-self-describing-surface capture-drafts-cannot-publish cli-experience codegraph-cache-effectiveness eka-improvements eka-mcp-production embed-parity-enforcement exchange-export-atomicity git-discovery-unbounded-walk high-risk-test-coverage knowledge-freshness-semantics knowledge-sharing mcp-destructive-confirmation-parity parallel-container-execution plugin-install-trust prd release-coordination team-collaboration team-collaboration-phase2 tool-dispatch-decomposition version-surface-drift
str: self-hosting
vis: vision
adr: 001-identity-serialization 002-state-vector-encoding 003-projection-model 004-phase-as-metadata 005-dimension-layout 006-exchange-conventions 007-extension-research-finding 008-engineering-domain-model 009-knowledge-runtime-architecture 010-synchronization-model 011-immutable-engineering-knowledge-model 012-canonical-knowledge-object-runtime 013-store-backed-projections 014-runtime-interface-architecture 015-machine-retrieval-interface 016-standard-v2-json-authoring 017-repository-identity-metadata 018-repository-context 019-work-item-transitions-notes-gates 020-repository-bootstrap-namespace-reconciliation 021-context-engine 022-ai-skill-pack 023-skill-pack-install-command 024-self-update-command 025-instance-resolution-latest 026-feedback-module 027-source-only-snapshot-transport 028-plugin-install-trust-model 029-assigned-work-membership 030-mcp-contract-relocation 031-plugin-command-registration 032-runtime-snapshot-adoption 033-llm-context-efficiency 034-code-graph-derived-cache 035-inferred-direct-task-capture 036-repo-scoped-status 037-execution-state-cko 038-telemetry-capture freshness-validity-horizon sharing-object-model solo-author-model standard-version-1-4-corpus steward-acp-bridge steward-boundary steward-determinism-boundary steward-security-baseline steward-worker-role-model
... +11 more (eka status)
<!-- eka:workflow:end -->
