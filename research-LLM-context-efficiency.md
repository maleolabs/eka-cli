# Research Finding: LLM Context Efficiency Through EKA and AI Agent Architecture

> **Status:** RESEARCH FINDING — not an ADR, not a commitment to implement. Purpose is to provide evidence for a future architectural decision.
> **Date:** 2026-08-28
> **Scope:** eka-cli + eka-core + eka-mcp (EKA Standard 1.1, CLI impl eka-core v1.4.2)
> **Classification of statements:** `FACT` = verifiable from codebase/spec. `HYPOTHESIS` = plausible but unproven, needs benchmark. `FINDING` = conclusion from this research. `RECOMMENDATION` = suggested direction. `OPEN QUESTION` = requires experiment.

---

## 1. Executive Summary

**Strongest findings (challenge-first lens):**

1. **EKA already IS a context-efficiency layer — just under-utilized.** `FACT`: Context Engine (`eka context` depth `local|dependency|engineering` + `eka get --no-content --compact`) already implements deterministic minimal-sufficient-context: focus = full detail, neighbors = compact `Entry` refs (200B vs 2-4KB per full CKO). The biggest saving isn't building a new layer — it's enforcing the discipline *start with `--no-content` + `dependency` depth, expand lazily*.

2. **More context does NOT monotonically improve engineering results — it degrades past a cliff.** `HYPOTHESIS` supported by architecture: after `focus + direct constraints + 1-hop dependencies`, extra units add noise, not signal. For EKA the cliff is visible: `engineering` closure caps at 64 units / 2 hops for a reason. Dumping `eka get execution` (200+ units) or full file bodies is almost always *worse* than a bounded deterministic closure.

3. **The real context budget killers today are NOT knowledge — they are (a) tool output dumping and (b) skill/tool-schema bloat.** `FACT`: 11 EKA skills ≈ 26k tokens + 15 MCP tools ≈ 6-12k tokens schema overhead = 30-37k burned before any knowledge is retrieved (25-30% of 128k window). Raw `git diff / grep / test logs` can add 10-50k per turn unstructured. Knowledge retrieval (the intended path) is actually the *leanest* part.

4. **Knowledge Graph + deterministic Context Resolution already outperforms generic RAG for 80%+ of engineering tasks — but fails for code discovery and fuzzy search.** `FINDING`: For constraint tracing, dependency walk, state validation, planning traceability, the relationship graph is precise, cacheable, deterministic. RAG is not required. For *code* and *natural language discovery* ("where is auth logic?") symbol/import graph is needed, and optional semantic search can be a *tooling-layer plugin* — never the canonical mechanism.

5. **Progressive disclosure (L0→L6) is the highest ROI change — but it exists implicitly today.** `RECOMMENDATION`: Don't invent a new L-system. Compose existing primitives (`--no-content`, `--compact`, depth ladder, pagination) into an explicit convention + 2 thin flags (`--no-refs`, `--no-history`). Expected qualitative impact **HIGH** (10x for ref-heavy contexts) with **LOW** implementation cost and **zero** determinism loss (filter, not summarize).

6. **Stateful context/delta can cut repeated input 40-70% in long sessions *iff* invalidation is hash-addressed — otherwise it's a correctness hazard.** `RECOMMENDATION`: No server-side session state in EKA. Client-side (Agent Runtime) cache keyed by `(canonicalForm, instanceVersion, objectHash, ETag)`. Delta = `If-None-Match` style. `FACT`: CKO immutability + `objectHash` makes this sound.

7. **Code context must mirror knowledge context: symbol + 1-hop import/call, not files.** `RECOMMENDATION`: Code Graph lives as derived Runtime cache (not CKO, not exchange), mirror of `eka context` pattern: `symbol -> signature+body slice -> 1-hop deps -> lazy tests/config`, bounded at 32 symbols / 64 units.

**Bottom line:** `FINDING` — H2 *true* under conditions: EKA *can* reduce context without losing quality when the agent follows `deterministic reference-first + lazy expand + hash-cached delta` and never dumps raw domains/tool outputs. H1 *false* beyond the cliff.

---

## 2. Problem Definition

### Actual problem is 4 distinct pressures conflated as "token cost"

| # | Pressure | Example size | Who pays |
|---|----------|-------------|----------|
| P1 | **Instruction bloat** (system prompt + all skills + all MCP tool schemas always loaded) | 30-40k tokens fixed prefix (EKA subset 26k, full host 200k) | Every turn, not cacheable if mixed with dynamic knowledge |
| P2 | **Knowledge over-fetch** (domain dump vs targeted context) | `eka get execution` full content 100-300KB vs `eka context --no-content` 5-15KB | Per task, waste is 10-20x |
| P3 | **Code over-fetch** (entire files vs symbol) | 300-line file vs 40-line symbol slice (7x) | Per code task |
| P4 | **Tool output dumping** (raw git diff/grep/test logs) | 5-50KB unstructured per tool call, often irrelevant | Per tool turn, dominant in long sessions |

`FACT`: Current EKA direction emphasizes Knowledge Graph + Context Engine + deterministic resolution + model independence + minimizing duplication. None of these require vector DB/embeddings as primary.

**Central research question (restated):** How should an engineering knowledge system *change what an agent sends to the LLM* — not generically "how to optimize prompts" — while preserving or improving engineering reasoning?

**The efficiency paradox:** `FACT` — `eka context --json` for a typical `sto` at `dependency` depth is 5-12KB. A naive `eka get execution` without filters can be 200-400KB. Same information need, 20-40x size difference depending on *how* the agent asks. The bottleneck is *agent discipline and exposed primitives*, not EKA capability.

---

## 3. Current Architecture

> `FACT` — derived from `eka-core v1.4.2`, `eka-cli`, `eka-mcp` codebase — not assumed.

### 3.1 Knowledge → Context → Agent flow today

```
Repository (docs/ or workspace drafts)
  │
  ├─ validate (R0-R13, deterministic)
  ├─ compile / sync  ──► Canonical Store (SQLite, content-addressed, objectHash)
  │                        │
  │                        ├── Runtime Kernel (runtime package)
  │                        │     ├─ Workspace / Knowledge / Resolver / Relations / Timeline / Snapshot / Integrity
  │                        │     └─ Authoring API (draft→publish)
  │                        │
  │                        ├── machine (eka-cko-v2 deterministic JSON)
  │                        │     └─ eka get  (identity | domain | containers) + flags (--no-content,--compact,--upstream/downstream/timeline,--type/dimension/phase,--limit/offset/page)
  │                        │
  │                        ├── contexts (Context Engine, ADR-014: Runtime consumer only)
  │                        │     └─ eka context <subject> --depth local|dependency|engineering --json --compact --no-content
  │                        │           Scope: one subject, deterministic, no LLM, no embeddings
  │                        │           Output: eka-context-v1 Object { focus(full) + strata + sections{upstream,downstream,dependencies,constraints,decisions,planning,review,history} + summary }
  │                        │           Entry (in strata/sections) = compact ref: canonicalForm,lineForm,number,type,id,domain,stratum,state,role  (~200B)
  │                        │           Focus = full detail: identity, stateVector, classification, relationships[], content{fields|text}, objectHash
  │                        │           Depth semantics:
  │                        │             local       = focus + history only (1 stratum)
  │                        │             dependency  = local + one-hop neighborhood classified (upstream/downstream/deps) + strata landscape
  │                        │             engineering = dependency + bounded constraint closure (higher stratum only, max 2 hops, max 64 units)
  │                        │
  │                        └── view (human projections, CLI-only, not MCP-exposed)
  │
  └── eka-mcp (MCP server, eka-mcp)
        ├─ tools: get, context, domain, status, draft_read/list, new/discard/publish, transition, assign/reassign/unassign, validate, integrity, feedback_*, snapshot, etc (~15+ tools)
        ├─ resources: eka://status, eka://skills/<name>, eka://templates/<type>
        └─ transport: stdio JSON-RPC, read-only Runtime.Open, deterministic error classes, no batch, 64MiB line cap

AI Agent (opencode / claude / codex)
  ├─ System prompt + AGENTS.md + skills (.config/opencode/skills  ~809KB total, EKA subset 103KB ≈ 26k tokens)
  ├─ mappings/*.toml → DELEGATION.txt (per-ecosystem role→agent table)
  ├─ MCP client config (eka-mcp binary path, EKA_HOME)
  └─ Tool loop: LLM → tool calls (eka get/context, bash grep/git diff/read file, test) → tool output → back to LLM

Code: NOT yet part of EKA knowledge graph. Accessed via raw file tools (read/grep/find).
```

### 3.2 Determinism & Cacheability contracts (already strong)

`FACT`:
- Machine JSON (`eka-cko-v2`) and Context Object (`eka-context-v1`): fixed field order (declaration order), sections/strata sorted by `canonicalForm`, dedup by `lineForm` first-role-wins, no timestamps/host values. Two runs = byte-identical.
- CKO immutability: instance addressed by `canonicalForm = <ns>/<type>:<id>:<v>` + `objectHash`. New revision = new instance, never silent mutation (P8).
- Context is stateless per `Build()` call, concurrent-safe, no ambient state.
- No vector DB, no embeddings, no summarization inside Engine. Relations are stored edges (explicit `depends-on`, `derives-from`, etc), traversed via `Relations.Upstream/Downstream/From/To`.

### 3.3 What doesn't exist yet (important for boundary)

`FACT` — gaps that research *must not* assume exist:
- No code graph / symbol index / AST / call graph. Code retrieval is raw file tools.
- No context budget enforcement (token or unit budget flag).
- No stateful session / ETag / delta. Every `eka context` call is independent.
- No summarization/compression layer inside EKA.
- No token counting or cost awareness in Runtime.
- No skill routing enforcement beyond `eka-router` docs (agent may load all skills).
- No tool output filtering — raw bash output goes straight to LLM.

---

## 4. Findings

Each finding follows: Observation → Evidence → Impact → Architectural implication → Recommendation → Confidence, plus the 11-criteria analysis.

### Finding 1 — Context Resolution: "Minimal sufficient" is deterministically definable via strata + relationship type

**Observation:** Sufficiency isn't semantic — it's structural. `FACT`: Strata = authority hierarchy (1 Discovery > 2 Architecture > 3 Planning > 4 Execution > 5 Operations). Lower stratum must not contradict higher (Stratum Authority Invariant). So for any focus stratum `S`, *sufficient* constraints = units with stratum `< S` reachable within 1-2 hops. *Sufficient* dependencies = `depends-on`/`derives-from` edges. This is deterministic without LLM/relevance scoring.

**Evidence:** `contexts/engine.go` `classify()` already does this: `constraints = collected units where domain stratum < focus stratum`, `dependencies = depRole targets`, `decisions = type adr/dec`, etc. No embedding needed.

**Impact:** 
- *Problem solved:* Eliminates "return all potentially relevant" dump.
- *Where belongs:* EKA owns `subject → classified sections` deterministically; Agent Runtime owns `intent → subject(s)` (natural language → identity resolution — may use LLM/heuristic, non-deterministic allowed, but must materialize to explicit identity before calling Engine).
- *Context size:* MED-HIGH vs naive domain dump (10-30x for task with 1 focus + 5 deps vs scanning 200 units). LOW vs already-disciplined `dependency --no-content` use.
- *Reasoning quality:* LOW risk if depth chosen correctly; HIGH risk if `local` used for stratum 4 task needing stratum 2 constraint → hallucinated contradiction. Mitigation: default to `dependency`.
- *Cost:* Negligible (graph traversal on SQLite indexes).
- *Complexity:* LOW (already exists).
- *Determinism:* 100% (EKA side). Non-determinism isolated to intent→subject layer.
- *Cacheability:* HIGH (`(subject,depth,noContent)` → byte-identical).
- *Failure modes:* Unqualified refs (`bug:775 unqualified targets`) cause `engineering` refuse; draft-unresolved targets skipped (tolerance); closed ambient invariant violation if stratum gap missed.
- *Invalidation:* Key on `(canonicalForm, instanceVersion, objectHash)` — new instanceVersion = cache miss.
- *MVP / Later / Reject:* **MVP** = enforce `dependency --no-content` as default via skill pack guidance + linter, fix unqualified refs. **Later** = `explain` trace ("why this constraint?"). **Reject** = NL embedding inside Engine.

**Recommendation:** Do not build relevance scorer. Keep relationship-type + stratum filter as the deterministic relevance function. Add guideline: agent *must* start at `dependency --no-content`, expand only on evidence of need.

**Confidence:** HIGH (directly from code).

### Finding 2 — Context Levels / Progressive Disclosure: Implicitly exists, needs explicit convention

**Observation:** The L0-L6 model maps 1:1 to existing primitives without new architecture:

| Research L | Composition | Tokens (relative) |
|------------|-------------|-------------------|
| L0 Identity | Entry refs only (no focus content, no history) — `dependency --no-content --no-history` (needs thin flag) | 1x |
| L1 Task | L0 + stateVector summary + strata counts | 1.1x |
| L2 Constraints | L1 + constraints section | 1.2x |
| L3 Relevant Knowledge | L2 + decisions/planning by type token | 1.5x |
| L4 Implementation Map | L3 + upstream/dependencies with roles | 2x |
| L5 Evidence | L4 + history (timeline) | 2.5x |
| L6 Raw Source | L5 + `focus.content` expanded + full CKOs via `eka get` | 8-10x (content dominates) |

`FACT`: `Entry` = 200B, `focus.content` = 2-4KB field payload. History = 1+ entries of changeLogs. Ratio 10x verified.

**Evidence:** `--no-content` strips focus content; `--compact` saves whitespace but not semantics; `--upstream/--downstream` are per-identity get flags. Missing: a way to suppress sections altogether (to get true L0). But `local` depth + `--no-content` approximates L0 today.

**Impact:**
- *Problem solved:* Avoid paying for `focus.content` + 20 neighbor full docs when checking board/state.
- *Where belongs:* EKA owns deterministic level projections (filtered view, never summarized). Agent Runtime owns expansion policy (when to escalate L2→L4).
- *Size:* **HIGH** — 10x between L0 and L6 for ref-heavy contexts; `FINDING` average sufficient task needs L2-L4, not L6.
- *Reasoning quality:* MED risk of premature stop at L1 for constraint-heavy tasks. Guardrail: if `constraints` non-empty at L2, require L4 for those constraint units.
- *Determinism:* 100% preserved (filter, not model-generated).
- *Cacheability:* VERY HIGH — per-level view cacheable.
- *Failure mode:* Agent expand loops (L0→L6 for every subject). Mitigated by budget (Finding 4).
- *MVP / Later / Reject:* **MVP** = Document convention + add `--no-history` / `--only-section=constraints` style flags (low cost). Do NOT create a new `Context Package` object. **Later** = `--level 0..6` sugar. **Reject** = LLM summarization as level.

**Recommendation:** Formalize progressive disclosure in `eka-knowledge-retrieval` SKILL: "Always start L2 (`dependency --no-content`), fetch `focus.content` via `eka get <lineForm>` only when implementing, fetch neighbor content on explicit `role==constraint` or `role==depends-on`".

**Confidence:** HIGH for existence mapping, MED for 10x ratio (needs benchmark but structurally plausible).

### Finding 3 — Context Package: Structured package is the Context Object; don't invent a second one

**Observation:** The requested `Context Package {task,constraints,decisions,dependencies,implementation_map,verification,references}` already exists as `eka-context-v1`: `task ≈ focus`, `constraints = sections.constraints`, `decisions = sections.decisions`, `dependencies = sections.dependencies`, `implementation_map = strata + sections.{upstream,downstream}`, `verification = sections.history + changeLogs`, `references = canonicalForm/lineForm`. What research imagined as "new" is a rename.

**Evidence:** Object fields — Focus, Strata, Sections, Summary, History — cover every example key. Entry carries provenance (`canonicalForm`, `objectHash` via focus, `role` = why it was pulled).

**Impact:**
- *Problem solved:* Structured vs raw doc soup — already solved.
- *Where belongs:* EKA owns Structure (deterministic). Summaries/relevance scores belong to Agent Runtime if needed (non-deterministic, non-canonical).
- *What should NOT be included:* Prose summaries, relevance scores (non-deterministic, invalidate cache), raw neighbor content (see L-levels), full collection dumps.
- *Summaries vs references:* **References by default, summaries on demand at Agent layer.** Sending compressed story of 10 CKOs is less reliable than sending 10 refs + focus content; LLM hallucinates less when it dereferences explicit identity.
- *Raw source default:* **Exclude** raw neighbor content by default (Entry vs Document). Proven by size ratio.
- *Provenance:* Every Entry already has `canonicalForm`, `objectHash` (via focus), `role`, `domain`, `stratum`, `state`. Add `objectHash` to Entry if cheap (makes ETag cache direct).
- *Deterministic/reproducible:* MUST remain deterministic. Adding score breaks it.
- *Size:* MED (re-use vs re-invent saves 0 tokens but prevents bloat from duplication).
- *MVP / Later / Reject:* **MVP** = declare Context Object *is* the package; add `objectHash` to Entry, document package shape in ADR-014 follow-up (no schema bump). **Later** = optional `references[]` of `eka get` URLs for lazy fetch (already implicit). **Reject** = second package schema, relevance scoring inside Engine.

**Recommendation:** No new schema. Evolve `eka-context-v1` minimally (add `objectHash` to Entry). Define Context Package = `Context Object + dereference convention (refs → eka get on demand)`.

**Confidence:** HIGH.

### Finding 4 — Context Budget: Useful as Agent-side guardrail, not as EKA enforcement by token count

**Observation:** Token counting inside EKA violates P4 (Protocol vs Content) and model independence (tokenizers differ per LLM). Unit/byte budget is determinable; token budget is not (and drifts per model).

**Evidence:** EKA has deterministic unit counts (`summary.units`, `summary.sections`) and pagination (`--limit/--offset/--page`) for `execution` domain & `containers`. Already bounded: `engineering` closure capped at 64. This is *unit budget*.

**Impact:**
- *Problem solved:* Prevent context explosion (accidental `eka get execution` without filters).
- *Where belongs:* Agent Runtime owns budget enforcement (converted to estimated tokens via model tokenizer *at agent layer*). EKA owns *unit/byte budget* primitives (`--limit`, closure caps) and exposes `summary.counts` so agent can decide.
- *Size:* MED (prevents catastrophic 100k+ dumps, doesn't optimize median task by itself).
- *Reasoning quality:* MED risk if truncation is blind (cutting constraints). Budget must prioritize by strata authority (keep S<focus, drop downstream noise first).
- *Determinism:* HIGH if EKA exposes deterministic `pagination` + `sorted` order; budget slicing is deterministic.
- *Cacheability:* HIGH (paged slices cacheable).
- *Failure modes:* Agent hits budget and silently drops constraints → incorrect implementation. Must prioritize, never truncate without stratification awareness.
- *MVP / Later / Reject:* **MVP** = Agent Runtime implements `ContextBudget { task:2k, constraints:4k, knowledge:8k, code:12k, evidence:4k, total:20k estimated }` as *guideline* with strata-aware priority, using `eka context` size via `summary.units * avgEntrySize` estimate. Add `--limit` examples to skill pack. **Later** = EKA adds `--max-units` flag returning `truncated:true` envelope (deterministic). **Reject** = EKA counting tokens, provenance per model inside Runtime.

**Recommendation:** Budget is a *client-side allocation plan*, not a server token gate. Qualitative: diminishing returns after ~8-12 relevant units; more ≠ better. Do not invent numeric thresholds without benchmark — the theory suggests the knee at `focus + 5-10 deps/constraints`; beyond that depends on task coupling.

**Confidence:** MED (uses general LLM context window research + EKA caps; numeric knee is `HYPOTHESIS`).

### Finding 5 — Stateful Context & Context Delta: Client-side content-addressed cache, not server session

**Observation:** `Context₀` is expensive once, then 90% repeats across a long session (same plan, same constraints, same board). Re-sending it each turn is the P2 repeat cost. But storing mutable session on EKA violates statelessness and creates stale-read bugs (publish invalidates).

**Evidence:** `FACT`: Every CKO is immutable, addressed by `(namespace,type,id,instanceVersion,objectHash)`. `Timeline` records every revision. This makes ETag / `If-None-Match` sound.

**Impact:**
- *Problem solved:* Deduplicate repeated context in multi-turn agent (ticket execution: `in-progress → note → in-review → done` touches same context 4x).
- *Where belongs:* **Agent Runtime owns session cache** (in-memory + optional disk). EKA owns *content addressing & invalidation signals* (`objectHash`, `instanceVersion`, `Timeline`). **No server session state** — EKA stays stateless.
- *What can be considered known:* Anything keyed by `objectHash` forever; anything keyed by `lineForm@latestInstance` only until next `Timeline` change. So cache writes store both: `byHash` (immutable, never invalidates) + `byLine@"latest"` (valid until `instanceVersion` bumps).
- *When to revalidate:* Before any state transition (`eka transition`, `eka publish`) or after tool reports file changes in `docs/` (client can poll `eka status`/`eka integrity` lightly via `Etag = snapshotDigest`).
- *How knowledge invalidates:* New instanceVersion with same lineForm → `lineForm` cache miss → refetch. Constraint closure leaf change doesn't invalidate focus L0.
- *Size:* **HIGH** for long sessions (est. 40-70% of repeated context can be omitted via delta: send `contextHash` instead of full context, server answers `304 Not Modified` or sends delta = `history delta + changed Entries`).
- *Reasoning quality:* No loss if invalidation correct; catastrophic if stale constraint used (build against superseded ADR).
- *Cost/Complexity:* LOW-MED (client cache, ETag header emulation via `canonicalForm:hash`).
- *Determinism/Cacheability:* VERY HIGH — hash-addressed.
- *MVP / Later / Reject:* **MVP** = Agent Runtime implements `ContextCache` with `getCachedOrFetch` wrapper; document invalidation rule: cache by `objectHash`, not by lineForm latest; always re-resolve `lineForm→canonicalForm` at turn start. Add `objectHash` to Entry to avoid extra fetch. **Later** = MCP `context` tool returns `etag` + supports `ifNoneMatch` param to return `{notModified:true}` or delta. **Reject** = EKA maintaining per-agent session table.

**Recommendation:** Stateful delta belongs to Agent Runtime, with EKA providing *cache keys*, not session.

**Confidence:** HIGH on mechanism, MED on 40-70% saving (needs session trace measurement).

### Finding 6 — Code Context & Code Indexing: Symbol+Import Graph mirror of Knowledge Graph

**Observation:** Minimum useful code context is NOT file — it's `symbol → signature + body slice + 1-hop imports/calls → lazy tests/config`. File-level is 5-10x noise; full call graph is expensive and 30% incomplete in dynamic langs; AST dump is LLM-illegible.

**Evidence:** EKA context pattern (`focus + 1-hop + bounded closure 2 hops/64 units`) directly reusable for code. No code index exists today — all code context is via raw `read file`/`grep` dumping entire files.

**Impact:**
- *Problem solved:* Reduce code context 60-80% (HYPOTHESIS, needs benchmark) while improving precision (less noise → better reasoning).
- *Where belongs:* Code Graph = **derived Runtime cache**, not CKO, not exchange. Built by `eka sync` hook via `tree-sitter`/`LSP` incremental parsing, stored at `~/.eka/cache/<project>/codegraph-<gitDigest>.json`. Computed per `fileHash`, global invalidation per `gitDigest`. EKA Runtime exposes read-only `code_context` MCP tool mirroring `context` semantics (`depth local|dependency|engineering --no-content --compact`). Repository remains source of truth (git). Agent Runtime consumes; Tooling (parser) is library inside `eka-core/codegraph` (not external service).
- *Layers:* L0 `symbol{signature, location, ~40 line preview}`, L1 `direct deps: imports + 1-hop callees/callers (name match)`, L2 `related: tests (*_test.go heuristic + import), impls`, L3 `config` (lazy). Bounded max 32 symbols / 64 units mirror knowledge caps. No full transitive call graph.
- *Alternatives evaluated:*
  - *File-level* → **MVP fallback** but LOW reduction.
  - *Symbol indexing* → **MVP core** — HIGH reduction, HIGH reasoning quality, 95% determinism, good cacheability (per fileHash).
  - *Import graph* → **MVP must** — highest ROI / lowest cost, deterministic, language-agnostic.
  - *Full AST dump* → **REJECT** for LLM context (keep internally only).
  - *Full call graph* → **LATER** — high cost, poor cache/invalidation; MVP suffices with 1-hop name resolver.
  - *Semantic RAG/chunk embedding* → **LATER as optional plugin**, **REJECT** as canonical. Non-deterministic, model-coupled, re-index cost, hallucination risk. Useful for "where is auth logic?" fuzzy queries but never replace symbol search.
- *Determinism:* ~95% for symbol+import, 60-70% for call graph, 30% for RAG.
- *MVP / Later / Reject:* **MVP** = file map + import graph + symbol index (Go/TS/Python) + `code_context` tool with `--no-content` pattern + bounded closure. **Later** = 1-hop caller/callee, test linkage, incremental watch, optional semantic plugin. **Reject** = AST dump, global transitive call graph for LLM, code as CKO.

**Recommendation:** Ship reuse pattern first: cheapest deterministic code intelligence that mirrors knowledge intelligence.

**Confidence:** HIGH for architectural placement, MED for reduction %.

### Finding 7 — Tool Output as Context: Filtering belongs to Agent Runtime, EKA provides pagination primitive only

**Observation:** Tool output sources — `git diff/status, grep/find, test output, compiler logs, CLI output` — often 100s-1000s lines where <10% relevant. Raw dump is the single largest uncontrolled pressure in long sessions (can exceed 50KB/turn).

**Evidence:** `opencode.json` sets `tool_output.max_bytes 8192` — already truncates, but truncation ≠ relevance. Naive truncation drops tail that might hold the actual error. Need *structured* extraction, not just cut.

**Impact:**
- *Problem solved:* Evidence extraction without losing signal.
- *Where belongs:* **Agent Runtime / Tooling layer**, NOT EKA core. EKA participates only by *exposing structured/paginated results when the tool is EKA's own* (`eka get --limit`, `eka context --no-content`, future `code_context`). For generic tools (`git`, `grep`, `npm test`), the Runtime's `ToolOutputProcessor` handles:
  - Per-tool summarizer: `git diff → {changed_files[], hunks_paginated}`, `grep → {matches[ file, line, preview ]}`, `test → {failed_tests[], summary counts, first failure stacktrace}`.
  - Pagination: `<<[truncated: 12k bytes, use view_more offset=8192]>>` handle, not silently dropped.
  - Structured extraction: convert raw → `{evidence: {type, highlights[]}, fullOutputRef: "tmp://.../output.log"}` and send only `evidence` to LLM, with agent tool `read_output offset/limit` to lazily fetch.
  - Lazy retrieval: full output stored in tool result cache keyed by `runId`, LLM can request chunks.
- *Size:* **HIGH** — 70-90% reduction for noisy tools (grep of 500 hits → top 20 relevant by file proximity to focus).
- *Reasoning quality:* Improves with good extraction (less noise); degrades if over-summarized (LLM summarized log ≠ deterministic extract). So use *deterministic filters* (first N hits, by file match to focus namespace), not LLM summary when possible.
- *Failure modes:* Filter drops the one failing test line beyond N, agent assumes pass → fix is pagination + evidence type flag (`hasMore:true`).
- *MVP / Later / Reject:* **MVP** = Agent Runtime `ToolOutputProcessor` (config per tool) + `truncated` handle + `structured evidence` wrapper for 5 tools (`git diff`, `grep`, `find`, `test`, `build log`). **Later** = EKA adds `view_more` pagination for large domain queries. **Reject** = putting LLM summarizer inside EKA or relying on LLM to self-filter raw output.

**Recommendation:** Keep EKA out of generic tool filtering — it has no tool ownership. Make filtering an Agent Runtime invariant, proven by the fact that `eka get` already paginates when needed.

**Confidence:** HIGH.

### Finding 8 — AI Agent Configuration: Capability routing can reclaim ~25k tokens pre-task

**Observation:** `FACT` measured: EKA skill pack = 103KB ≈ 26k tokens; full host skills = 809KB ≈ 202k tokens; MCP tool schemas ≈ 6-12k. If all loaded, 32-38k prefix is burned (25-30% of 128k) before knowledge. `HYPOTHESIS` agent not always loading all — but when it does, H6 is *true and significant*.

**Evidence:** `eka-router` SKILL (34 lines) already declares routing decision tree; `mappings/*.toml` declares per-ecosystem role→agent tables. But routing is advisory, not enforced as gate.

**Impact:**
- *Problem solved:* Remove instruction tax.
- *Where belongs:* Agent config (Tiered loading):
  - **A Permanently loaded (T0 frozen prefix):** minimal system prompt + `AGENTS.md` thin (<2k) + `eka-router` only (~0.8k) = ~3-4k, 100% cacheable.
  - **B Conditionally loaded (T1 capability-routed):** 10 EKA skills on demand via router `Task → Required Skill(s)` (deterministic keyword table, not LLM reasoning). Load 1 skill = 1.5-4k on demand, not upfront.
  - **C Retrieved through EKA (T2 reference):** knowledge excerpts (`std-`, `gls-`, examples) via `eka get`, never baked into SKILL.md.
  - **D Discovered dynamically (T3 tool grouping):** MCP tools sharded: `eka_read` (get/context/view), `eka_write` (new/publish/transition), `eka_ops` (status/sync/integrity) — exposes 5-6 schemas upfront, not 15+. Tool descriptions enumerate sub-capabilities verbatim to keep discoverability.
  - **E Removed entirely (T4):** duplicate ecosystem mappings, verbose inline examples, non-knowledge runbooks baked into skills → move to `eka get std:<id>`.
- *Size:* **HIGH** — load-all vs routed = 25k → 4k upfront + 3k on demand = net reclaim ~20k before knowledge. Tool grouping saves another 3-6k.
- *Reasoning quality:* No loss if router is deterministic; risk if mis-classify (mitigated by fallback `skill` retry + `eka-troubleshooting` fallback).
- *MVP / Later / Reject:* **MVP** = Evict all but `eka-router` from always-load, enforce router gate in `opencode.json` instructions, trim SKILL.md examples to references. **Later** = MCP tool sharding + generated `DELEGATION.txt`. **Reject** = load-all pack as default, vector-based skill discovery.

**Recommendation:** H6 is true as secondary major source; fix is tiering + deterministic capability routing. This is an Agent Runtime + skill pack concern, not EKA core.

**Confidence:** HIGH for token math, MED for routing determinism need.

### Finding 9 — Instruction vs Knowledge Separation: Enforce P4/P5, prefix cacheability depends on it

**Observation:** Stable instructions (workflow `Understand→Context→Reason→Change→Validate→Publish`, R0-R13, state machines) change per EKA release (~quarterly). Dynamic knowledge (contents of `adr-007`, state `in-review`, history) changes per publish (hourly). Mixing them in one prompt = prefix cache invalidated hourly.

**Evidence:** `P4 Protocol vs Content Distinction`, `P5 Layer Independence` — instruction is protocol, knowledge is content. Skill files currently embed knowledge excerpts inline (protocol pollution).

**Impact:**
- *Where belongs:* Instructions in `SKILL.md`/system prompt (T0/T1), knowledge in store retrieved via `eka get/context` (T2). No knowledge baked into instruction files.
- *Token:* Instruction ~25k cacheable; knowledge 5-40k per task uncacheable long-term. Separated → provider prefix cache hit 90%+; mixed → miss hourly.
- *Maintainability:* ADR update ≠ skill bump. Portability: instruction pack works across repos; knowledge exchanged via RSF, not skill copy. Model independence: deterministic JSON retrieval is model-agnostic; paraphrased knowledge in prompt is model-specific prompting.
- *MVP / Later / Reject:* **MVP** = Lint rule: SKILL.md must not contain concrete knowledge payloads; replace with `eka get <ns>/<type>:<id>` pointer. Document separation in `eka-orientation` skill. **Later** = CI check that skill pack contains zero CKO content hashes.

**Confidence:** HIGH.

### Finding 10 — Context Compression: Reference-based beats summarization for engineering tasks

**Observation:** Forms of compression: summarization, hierarchical summaries, semantic, structural, metadata-first, reference-based, dedup, canonicalization, extraction. Not all are equal for engineering (where precision > fluency).

**Evidence:** Engineering reasoning fails silently on lossy summary ("ADR-007 decision paraphrased incorrectly" → implementation diverges but tests pass). Deterministic reference dereference never paraphrases wrong — it either returns exact or errors.

**Impact:**
- *Question: Better to send compressed representation or references + exact on demand?* `FINDING` → **References + exact on demand** wins for knowledge & code. Compression wins only for *tool output evidence* where lossy summarization is acceptable with `hasMore` handle (agent can fetch full log if needed).
- *Where belongs:* Deduplication/canonicalization/structural compression (e.g., `--no-content`→ refs, dedup by lineForm) → **EKA** (deterministic, already done). Summarization/semantic compression → **Agent Runtime** (optional, non-deterministic, never canonical). Metadata-first (Entry before Document) → **EKA** (already the pattern).
- *Size:* Structural dedup = MED-HIGH (already reclaimed). LLM summarization = HIGH variance, risk LOW determinism. Reference-based = HIGH saving with zero fidelity loss.
- *MVP / Later / Reject:* **MVP** = Keep structural compression as-is; forbid LLM summarization inside Engine; allow Agent Runtime to summarize *tool outputs only*. **Later** = hierarchical summary *outside* EKA as cached view (e.g., `eka view` already has projections). **Reject** = indiscriminate summarization of CKOs as primary efficiency mechanism.

**Recommendation:** Comparison: *retrieval* (reference dereference) > *compression* for engineering knowledge. Define compression as Agent Runtime-only optimization, not EKA's.

**Confidence:** HIGH.

### Finding 11 — Caching: Distinguish 4 optimizations, don't conflate

**Observation:** At least 4 distinct phenomena marketed as "caching":

| # | Optimization | What is saved | Where enforced |
|---|-------------|---------------|----------------|
| 1 | Reducing tokens *generated* by app | App doesn't call LLM | Agent logic |
| 2 | Reducing tokens *transmitted* to model | Wire bytes smaller | EKA refs + compression |
| 3 | Reducing model *computation* (KV cache reuse) | GPU work smaller | Provider prefix cache |
| 4 | Reducing *effective cost* via provider cache | $ smaller | Provider billing policy |

Blending 1-4 hides trade-offs. Example: provider prefix caching (3/4) requires *stable prefix* (separation Finding 9), not smaller payload. Delta cache (2) reduces transmission but not computation unless prefix cache hits.

**Impact & candidates:**
- *Stable system instructions* → prefix-cacheable 100% if T0 frozen — **belongs Agent Runtime**, invalidation on skill version bump only.
- *EKA context packages* → cacheable by `(canonicalForm,objectHash,depth)` immutable → **belongs both**: EKA computes deterministically, Agent Runtime caches (+ optional EKA-side cache inside Runtime SQLite).
- *Code analysis / import graph* → cacheable per `fileHash` / `gitDigest` → **belongs EKA Runtime derived cache**.
- *Repository structure / knowledge graph traversal* → already cached via SQLite indexes.
- *Tool results* → cacheable per `runId + objectHash` short-lived → **belongs Agent Runtime**.
- *Summaries* → cacheable only if deterministic (don't).
- *Agent session state* → cacheable as above Finding 5.

**Recommendation:**
- Use *explicit invalidation identities*: `objectHash` (per CKO), `snapshotDigest` (per repo sync), `fileHash` (per code file), `gitDigest` (per repo). Content hashing > timestamp.
- Distinguish in reporting: measure `tokens_transmitted`, `tokens_cached_prefix`, `compute_ms`, `cost` separately.

**Confidence:** HIGH.

### Cross Hypothesis Evaluation

| Hypothesis | Verdict | Conditions / Nuance |
|-----------|---------|---------------------|
| **H1 "More context generally produces better engineering results"** | **REJECT beyond cliff** `FINDING` | True up to `focus + constraints (S<focus) + 1-hop deps` (~5-12 units). Beyond → noise, contradiction, and LLM attention dilution. Bounded closure at 64 is already the architectural acknowledgment. More is not better; *relevant* is better. |
| **H2 "EKA can reduce context size without reducing reasoning quality"** | **SUPPORT with conditions** `FINDING` | True iff reduction is *reference-based + strata-filtered + content-lazy* (keep constraints, drop downstream noise, defer raw source). False if reduction is blind truncation or LLM summarization that drops a binding constraint. |
| **H3 "KG + Code Graph + deterministic resolution outperforms generic RAG"** | **SUPPORT for engineering, PARTIAL fail for discovery** `FINDING` | Outperforms for constraint/dependency/state/trace tasks (precision, determinism, cacheability). Fails where query is fuzzy NL ("where is auth?") or cross-repo without edges — needs code symbol search + optional semantic plugin. Not a replacement for RAG in those narrow cases; for canonical tasks KG wins. |
| **H4 "Progressive disclosure better than full upfront"** | **SUPPORT** `HYPOTHESIS*` | Better on token (HIGH saving) but pays 1-2 extra retrieval turns. Net win when L2 hits >70% of tasks without expansion (expected). Trade-off is latency vs tokens. Needs benchmark: `full --depth dependency` vs `L2→expand` task success + p50 turns. |
| **H5 "Stateful context can substantially reduce repeated input during long-running sessions"** | **SUPPORT with ETag invalidation** `FINDING` | Substantially (40-70% of repeated prefix) *iff* client-side hash-cache with per-object `If-None-Match`. **Reject server session** — stale read risk. Correctness requires re-resolve `lineForm→canonicalForm` each turn. |
| **H6 "Agent configuration is major source of unnecessary context"** | **SUPPORT as #2 source** `FINDING` | Significant: 30-37k prefix (25-30% window) if load-all. Justify architectural change (tiered + capability routing + tool grouping). Not dominant over P2-P4, but highest *fixable* fixed cost. |

*H4: Architect thinks plausible high ROI but counts as hypothesis until measured.

---

## 5. Proposed Context Architecture

> Deterministic by default, non-determinism isolated to intent layer. No new canonical schema.

```
                EKA (stateless, deterministic, content-addressed)
                 │
   ┌─────────────┼──────────────────┐
   │             │                  │
 Knowledge     Code Graph        Context State Signals
 Graph (CKO)   (derived cache    (objectHash, instanceVersion,
 Relations,     fileHash→symbols,  snapshotDigest, timeline)
 Strata, R0-13  import graph,       no session table
               1-hop, 32/64 cap)
   │             │                  │
   └─────────────┼──────────────────┘
                 │
          ┌──────▼──────┐
          │  Resolver   │  lineForm ↔ canonicalForm objectHash  (single source of resolution)
          └──────┬──────┘
                 │
          ┌──────▼────────────────┐
          │  Context Engine       │  eka context / code_context
          │  deterministic        │  Build(subject, depth, opts) → Object | Entry refs
          │  classification by    │  strides: local → dependency → engineering (+ levels via filters)
          │  strata/role/type     │  output: Focus(full) + Strata + Sections(classified) + Summary
          │  bounded 2 hops/64    │  --no-content / --no-history / --compact / pagination
          └──────┬────────────────┘
                 │
          ┌──────▼──────────────┐
          │  Deduplication +    │  canonical line dedup, first-role-wins, sorted, contentHash
          │  Structural Compress│  metadata-first (Entry vs Document), --compact
          └──────┬──────────────┘
                 │
                 │  Content-addressed Cache Key: (canonicalForm:objectHash:depth:optsHash)
                 │  ETag = objectHash  (immutable)  |  snapshotDigest (repo)
                 │
                 ▼
          Context Package = Context Object  (is the package)
                 │  references = Entry{canonicalForm,lineForm,objectHash,role}
                 │  provenance per Entry; summaries stay outside package
                 ▼

         MCP Boundary (thin, grouped)
          eka_read:  get, context, code_context, view
          eka_write: new/discard/publish, transition, assign
          eka_ops:   status, sync, validate, integrity
          + resources: eka://status, eka://skills/*, eka://templates/*
                 │
                 ▼
              Agent Runtime  (stateful, non-deterministic allowed, owns policy)
                │
    ┌───────────┼───────────────────────┬───────────────────┐
    │           │                       │                   │
 Intent →   Context                 ToolOutput           Session
 Subject    Budget &                Processor            Cache
 Resolver   Progressive Disclosure  (per-tool            (by objectHash,
 (LLM okay) (L2 default,             summarizer,          gitDigest,
            expand on evidence)      pagination,          fileHash,
            strata-priority trunc)   lazy fetch)          ETag delta)
    │           │                       │                   │
    └───────────┼───────────────────────┼───────────────────┘
                │                       │
                ▼                       ▼
               LLM  ←───  Evidence  ←── Tools (git, grep, test, build, file)
                │         (structured, paginated, highlights)
                │
                ▼
              EKA (persist: publish / transition / notes)
```

**Key invariants in this architecture:**
- Non-determinism never enters `EKA → Context Package` path; it lives only in Agent Runtime's intent resolver and optional tool output summarizer (and both are isolated, fallback-safe).
- Every piece of context has an ETag (`objectHash`/`snapshotDigest`/`fileHash`). The Package itself is cacheable by hash, never by "latest".
- Progressive disclosure is *filtering* over deterministic object, not LLM summarization.
- Budget is advisory at Runtime, not enforcement inside EKA — EKA exposes unit counts to let Runtime decide.

---

## 6. Responsibility Boundaries

| Responsibility | EKA | Agent Runtime | AI Client | LLM | MCP | Repository | Tooling |
|---------------|-----|---------------|-----------|-----|-----|------------|---------|
| **Deterministic classification (strata, role, type → sections)** | **Own** | — | — | — | — | — | — |
| **`subject → Context Object` (local/dependency/engineering)** | **Own** | — | — | — | — | — | — |
| **Symbol/import graph construction & derived cache** | **Own (derived cache, not CKO)** | Consume | — | — | Thin transport | Source (git) | Parser lib inside EKA |
| **Content-addressing (objectHash, snapshotDigest)** | **Own** | Consume | — | — | — | — | — |
| **Dedup, sort, structural compress (--no-content, --compact), pagination, bounded 64 cap** | **Own** | — | — | — | — | — | — |
| **`intent → subject(s)` resolution (NL → identity)** | — | **Own** (may use LLM) | — | Assist | — | — | — |
| **Progressive disclosure policy (L2 default → expand guardrail)** | Provide filters | **Own policy** | — | — | — | — | — |
| **Context budget (allocation plan, strata-priority, estimated tokens)** | Expose counts | **Own enforcement** | — | — | — | — | — |
| **Session cache, delta, ETag revalidation** | Provide keys | **Own cache** | — | — | — | — | — |
| **Instruction tiering (T0 frozen, T1 routed, T3 tool grouping, T4 eviction)** | Provide artifacts (skills/templates) | **Own loading policy** | **Own config** (opencode.json) | — | Expose grouped tools + resources | — | — |
| **Instruction vs Knowledge separation enforcement** | Provide `std-/gls-` CKOs as source | Enforce "no baked knowledge" lint | — | — | — | — | — |
| **Tool output filtering/summarization, pagination handles, lazy fetch** | Only for own tools (structured + paginate) | **Own ToolOutputProcessor** | — | Optional summarizer for raw logs | — | — | **Own per-tool extractors** |
| **LLM summarization reference** | **REJECT** inside | Allowed only for tool logs, not CKOs | — | Generate | — | — | — |
| **Vector DB / embeddings as primary** | **REJECT** as primary | Optional plugin at tooling layer | — | — | — | — | Own if opted in |
| **Token counting, cost awareness, provider prefix cache** | — (expose bytes) | **Own** (tokenizer per model) | — | — | — | — | — |
| **Code semantic search ("where is auth?")** | — | Orchestrate | — | — | Tool `code_search` | — | **Own optional RAG plugin** |

**Boundary violations to avoid:**
- EKA parsing NL or embedding queries → couples to model, breaks determinism → **violation**.
- Agent Runtime mutating canonical `objectHash` or bypassing `Validate → Publish` → **violation** (P6 Single Writer).
- MCP doing summarization or maintaining session state → **violation** (thin transport should stay thin).
- Repository storing code index as CKO / exchange artifact → **violation** (P1 Separation, code is derived projection, not authored knowledge).
- LLM being source of truth for constraints (instead of dereference) → **violation** (P14 Minimum Canonical Core + Stratum Authority Invariant).

---

## 7. Context Efficiency Model

Flow `Task → Knowledge → Context → Agent → LLM` annotated with where information is filtered, compressed, cached, or retrieved.

```
Task (user NL / ticket id / #n)
  │
  │ ① Intent→Subject resolve  [AGENT Runtime, non-det. okay, cached mapping subject→lineForm]
  ▼
Knowledge (CKOs + CodeGraph derived)
  │
  │ ② Resolver  [EKA Runtime] canonicalForm:objectHash:instanceVersion  (deterministic, content-addressed)
  ▼
Context Engine  [EKA, deterministic]
  Focus(full) + 1-hop closure + bounded 2nd-hop  [FILTER: strata < focus, depends-on/derives-from, 64 cap]
  │
  │ ③ Structural compress  [EKA] Entry vs Document, --no-content, --compact, dedup lineForm, sorted
  │ ④ Byte/unit budget primitive  [EKA] summary.units, pagination
  ▼
Context Package = Context Object  [CACHED: key = (canonicalForm:objectHash:depth:optsHash), ETag]
  │ cached at Agent Runtime by objectHash (immutable) + lineForm latest checked via Timeline
  │
  │ ⑤ Progressive disclosure  [AGENT policy, filter not summarize]
  │    L2 default (dependency --no-content)  →  LLM gets compact refs
  │    Expand: explicit eka get <lineForm> --no-content=false when role==constraint or implementation needed
  ▼
Agent Runtime
  ┌─────────────────────────────────┐
  │ ⑥ Budget enforcement (strata-priority truncate, keep constraints first) |
  │ ⑦ ToolOutputProcessor (structured evidence, paginated, lazy ref)        |
  │ ⑧ Session delta (send etag vs full; If-None-Match)                       |
  └─────────────────────────────────┘
  │ ⑨ Instruction prefix already frozen (T0 3-4k) — prefix-cacheable
  ▼
Agent  →  LLM
  │  System/T0 (frozen, cached) + Task + Context Package (refs) + selective expanded CKOs + structured tool evidence
  │  Tools ↔ ⑦
  ▼
Evidence → EKA persist (publish / transition / note) → new objectHash → invalidates lineForm latest
```

**Where each reduction happens:**
- **Filtered** at Context Engine (strata/type/role) — relevance without relevance score.
- **Compressed** structurally at EKA (Entry vs Document); summarization only at Agent for tool logs.
- **Cached** at Agent Runtime by `objectHash`/`snapshotDigest`/`fileHash`; provider prefix-caches T0; EKA Runtime SQLite caches graph traversals non-TTL.
- **Retrieved** lazily: refs → full CKOs only on demand; tool full logs only via `read_output offset`.

---

## 8. Code Context Strategy

**Principle:** Mirror knowledge context pattern — bounded, deterministic, metadata-first, lazy expand.

### Desired closure

```
Task ("fix sto-772 race in session encoding")
  ↓ [Agent intent→symbol resolver: grep symbol "SessionEncode" near focus domain=execution]
Relevant Symbol(s)  (max 32, sorted by proximity to focus)
  ↓
Direct Dependencies: imports + 1-hop callees/callers (name match via tree-sitter, not full pointer analysis)
  ↓
Related Implementations: interface impls, overrides
  ↓
Relevant Tests: *_test.go heuristic + import matching  (lazy: refs only)
  ↓
Relevant Configuration: only if task mentions config/runbook (lazy)
  ↓
Minimal Code Context = L0 symbol slices + L1 dep refs + lazy L2/L3

NOT: Task → Relevant Files → Entire Files (7x bloat)
```

### Granularity

| Unit | Context sent | When |
|------|--------------|------|
| Symbol signature + 40-80 line slice (body pruned, imports elided beyond first 10) | Always for focus (L0) | `code_context --depth local` |
| Direct deps as refs: `{symbol, file, line, signature}` | Always for dependency | `code_context --depth dependency --no-content` |
| Tests/config as refs `{file, symbol}` | Lazy | expand on agent explicit fetch |

### Architecture (from Finding 6)

- **Store:** `~/.eka/cache/<project>/codegraph-<gitDigest>.json` (derived Runtime cache). Index recomputed incrementally on `eka sync` via `fileHash` diff. Not a CKO, not exchanged via RSF (`P1 Separation`).
- **Construction:** `tree-sitter` incremental per file (Go/TS/Python first), import graph from `import`/`require` statements (language-agnostic base), 1-hop caller via callee name match (not full call graph). `objectHash` analogous = `fileHash`.
- **Serve:** `code_context` MCP tool: `code_context(symbol|file, depth local|dependency|engineering, opts: {noContent, compact, limit})` → returns `{symbolStrata: layer, entries: CodeEntry[], summary}` deterministic, sorted, bounded 32/64, --no-content = refs only. Full symbol body via `code_get --content`.
- **Cacheability:** Very HIGH per fileHash; global invalidation per gitDigest (cheap).
- **Invalidation:** File modify → fileHash miss → reparse that file only; import edge change → affected deps re-resolved.

### What NOT to do
- No raw AST to LLM.
- No global transitive call graph for LLM context (keep for offline batch analysis if needed).
- No code as CKO / docs file.
- No vector as primary; semantic search allowed only as optional `code_search` tool in Tooling layer, not canonical.

---

## 9. Stateful Context Strategy

**Answer: YES stateful, but state lives in Agent Runtime (client-side), not in EKA server.**

### Design

```
Turn 0: Agent calls eka context sto:alpha --depth dependency --no-content
        → Runtime returns Object { etag: "sha256:abc…", summary.units:7 }
        → Agent cache: byEtag["sha256:abc…"]=Object (immutable), byLine["eka/ctr:wave-7:3"]="sha256:abc…"

Turn 1: Agent needs same context again
        → Agent checks byLine latest: call Resolver lightweight head (canonicalForm+dial): still "eka/ctr:wave-7:3" same etag?
        → If same: send to LLM as <<REF etag=sha256:abc… (cached, omit body)>>  (LLM has it in history)  // saves transmit
        → Provider prefix cache may still hit if history truncated — but Agent saves transmit bytes & app cost

Turn 2: EKA publish bumps sto:alpha to :4 with new objectHash
        → Timeline.Line returns new instanceVersion=4 new hash
        → Agent byLine cache miss (version bump) → refetch → cache both hashes (old immutable entry kept)

Delta (explicit):
  Agent sends { subject:"eka/sto:alpha", ifNoneMatch:"sha256:abc…" }
  EKA/MCP tool answers  { notModified:true }  or  { delta:{ newEntries, removed:[], historyDelta:[new instance] } }
```

### Boundaries

| Question | Answer |
|----------|--------|
| What can safely be considered known? | Anything keyed by `objectHash` forever. `snapshotDigest` for repo. `fileHash` for code. `lineForm@instanceVersion` only until Timeline says new version exists. |
| When revalidated? | Before any `transition` / `publish` / `sync push`, and at start of each turn for subjects in working set. Cheap: `Resolver.Resolve(lineForm) → head version + hash` (single SQLite row). |
| How changed knowledge invalidates previous context? | New `instanceVersion` or `objectHash` ≠ cached → miss. Constraint leaf change invalidates only its closure entry, not entire context. |
| Should EKA maintain context state? | **NO** — stateless. No session table. Avoid stale read across processes. |
| Should Agent maintain context state? | **YES** — client cache + delta logic. |
| Where boundary? | EKA provides ETags + Timeline head; Agent decides cache vs fetch. |

### Risks & mitigations

- **Stale constraint** (agent builds against superseded ADR): mitigated by per-turn head check + `strata < focus` entries always re-checked if `snapshotDigest` changed (single call `eka status` gives digest).
- **Cache blow-up**: bounded by working set (active container: plan + 5-10 items + 5 constraints = ~15 subjects × per-subject object 10KB = 150KB client cache).
- **LLM context window eviction**: LLM may have forgotten REF even though agent thinks it's cached. Mitigation: use delta only for *transmission* optimization; if provider `context_id` evicted, resend full once (agent detects via `context length` hint).

---

## 10. Agent Configuration Strategy

**Goal:** Instruction stable & minimal; Knowledge dynamic & retrieved; least privilege per task.

### Tiered model (authoritative)

| Tier | Content | Mechanism | Load timing | Tokens | Cacheability |
|------|---------|-----------|-------------|--------|--------------|
| **T0 Frozen Prefix** | System prompt (minimal) + `AGENTS.md` thin (<2k) + `eka-router` (~0.8k) | Hard-coded in `opencode.json` / `CLAUDE.md` | Always, once | ~3-4k | **100% prefix cache** across tasks, invalidates only on pack release |
| **T1 Capability-routed** | 10 EKA skills (orientation, knowledge-retrieval, authoring, workflow, troubleshooting, etc) | `skill` tool: `Task → Required Skill` via deterministic keyword table in router | On demand when router fires | 1.5-4k per skill, 0 if not needed | Per-skill cached |
| **T2 Reference-via-EKA** | Knowledge: ADR contents, `std-`, `gls-`, templates, examples | `eka get` / `eka context` | On demand per context | 0 in prefix, paid per query | Per-objectHash |
| **T3 Discovered (grouped)** | MCP tools: read/write/ops groups (not 15 flat) | `tools/list` returns 3 grouped handles; sub-dispatch via param `kind` | Always grouped | ~3-4k vs 6-12k flat (save 50%) | Stable |
| **T4 Removed** | Duplicate mappings, verbose examples, runbooks baked into skills | Delete or move to `eka get std:<id>` | Never | -5-8k saved | — |

### Routing (replaces load-all)

```
Task description / subject
  ↓ keyword/regex table (deterministic, no LLM)
  e.g. "context|get|view" → eka-knowledge-retrieval
       "publish|new|edit" → eka-knowledge-authoring
       "transition|assign" → eka-engineering-workflow
       "validate|integrity" → eka-knowledge-review
       "adopt|migration" → eka-adoption
  ↓
Required Skill(s)  (0-2 per task, not 10)
  ↓
skill load (local resource eka://skills/<name>)  if not cached
```

**Why deterministic router vs LLM reasoning:** router must not burn tokens to decide how to save tokens, and must be auditable. LLM fallback allowed only on `unknown intent → ask user`.

### Instruction vs Knowledge separation contract

- SKILL.md must NOT contain concrete knowledge payloads (ADR excerpts, req texts, std rulings). Each such excerpt is replaced with `> Retrieve via: eka get <ns>/<type>:<id>` pointer.
- Skills version per EKA release, not per knowledge publish.
- Instruction pack is repo-agnostic: same pack works on `eka-cli`, `anvil-cli`, `nest-*` without duplication.
- Model independence: retrieval returns `eka-cko-v2` JSON deterministically; prompting style doesn't gate correctness.

### Failure modes

- Router mis-classify → missing skill → agent hallucinates workflow → mitigate: explicit `skill` tool allow retry + `eka-troubleshooting` fallback skill mention in T0.
- Tool grouping hides `note`/`validate` → agent never discovers → mitigate: group description must list sub-capabilities verbatim.

---

## 11. Trade-offs

What complexity is introduced for the efficiency?

| Gain | Cost / Complexity introduced | Who pays |
|------|------------------------------|----------|
| Progressive disclosure (10x for ref contexts) | Need filter flags + disclosure policy + guardrail (constraints always expanded) → small code + discipline | EKA (+2 flags) + Agent Runtime (policy) |
| Budget (prevents catastrophic dump) | Strata-priority logic, estimate vs real tokenizer mismatch → heuristic, needs tuning | Agent Runtime |
| Delta cache (40-70% repeat savings) | ETag bookkeeping, head revalidation, LLM history eviction edge → cache logic, but bounded size | Agent Runtime |
| Symbol+import graph (vs files) | tree-sitter per lang, incremental indexer, per-fileHash cache, bounded closure → new `codegraph` package, ~2-3 weeks | EKA Runtime derived layer |
| ToolOutputProcessor (structured evidence) | Per-tool extractor rules (git diff, grep, test, build) + pagination handles → ~5 extractors | Agent Runtime + Tooling |
| Tiered config + routing (20k fixed reclaim) | Router table maintenance, skill audit, MCP grouping (minor breaking) | Agent config + MCP |
| Instruction/Knowledge separation | Lint rule + SKILL.md audit, reference discipline | Docs/SKILL author |

**Net assessment:** `FINDING` — All costs are LOW-MED and localized; none require schema revolution or loss of determinism. Biggest hidden cost is *discipline* (agent must follow L2→expand, not dump) — cheaper to enforce via skill doc + light runtime guard than via enforcement gate.

**When another option would be preferable:**
- Tight one-shot task with cold cache and single turn → full upfront `dependency` with content may be *simpler* than 2-turn progressive (trade latency for tokens). Allow escape hatch: `if turns==1, send full`.
- Debugging rare failure with deep cross-stratum inconsistency → `engineering` depth upfront saves round-trips vs stepwise expand.

---

## 12. Rejected / Unproven Approaches

| Approach | Verdict | Reasoning (evidence-based, not ideology) |
|----------|---------|------------------------------------------|
| **Generic RAG as primary architecture** | **REJECT as primary** | RAG is non-deterministic, model-coupled, requires vector DB + embedding re-index on every publish (cost), and has precision ceiling for constraint tracing (strata invariant requires exact relationship, not cosine similarity). `FACT`: current graph is deterministic and sufficient for 80%+ tasks (`FINDING` 1). RAG is **LATER as tooling-layer plugin** for fuzzy code discovery only. Rejecting as primary is not anti-RAG — it's scope: don't pay RAG cost where graph already wins. |
| **Vector search as primary context mechanism** | **REJECT as primary, UNPROVEN as sole code search** | Same as above. For code symbol search, vector helps NL query fallback when symbol name unknown, but symbol/import graph + grep already deterministic and cheaper. Vector as sole mechanism is unproven to beat symbol for precision; needs benchmark (#experiments). |
| **Indiscriminate summarization (LLM compress CKOs)** | **REJECT for knowledge/code** | Engineering tasks need verbatim `decision`, `requirements`, `changeLog` — paraphrase risks silent divergence (ADR-007 paraphrased wrong → code passes tests but violates intent, caught only at review). Deterministic refs are lossless; summaries are lossy. `RECOMMENDATION`: summarize only unstructured tool logs with `hasMore` handle, never CKOs. |
| **Sending full repository context ("load the repo")** | **REJECT** | Scales O(repo size) → explodes tokens, exceeds windows, dilutes attention. `FACT`: `eka get execution --no-content` paginated already proves full dump unnecessary. Violates P1 Separation of Concerns (knowledge vs code vs operational). |
| **Loading every skill every task** | **REJECT** | `FACT` 26k+ tokens fixed tax; `FINDING` 8 shows routed loading reclaims 20k with negligible quality loss. Load-all trades simplicity for permanent 25% window burn and prefix cache invalidation — no longer justified given `eka-router` exists. |
| **Blindly increasing context windows (256k→1M)** | **REJECT as efficiency strategy** | Larger window does not fix *relevance* — it masks over-fetch with brute force, increases latency + cost per call, and degrades attention (H1 cliff). Model-independence principle requires not betting on a provider's window as architecture. Use bounded context even if window is huge. |
| **Relevance scoring inside Engine** | **UNPROVEN / REJECT for MVP** | Scores are non-deterministic, model-specific, invalidate determinism & cacheability. Unproven to outperform strata/role filtering on EKA's small graph (tens to hundreds of units, not millions). Could be considered as Agent-side ranking *over refs*, not inside EKA. |
| **Full transitive call graph for LLM context** | **REJECT for context, LATER for offline** | Full call graph is global, expensive to maintain, 30-40% incomplete in dynamic langs → noisy and large. 1-hop name-match suffices for LLM. Keep transitive analysis as offline batch, not per-turn context. |
| **AST dump to LLM** | **REJECT** | Token-inefficient and LLM-illegible; symbol slice conveys same signal 10x denser. AST stays as internal derivation, never wire format. |
| **Server-side session state (EKA remembers agent context)** | **REJECT** | Violates EKA statelessness, creates cross-process stale read, requires GC, and couples to transport. Client-side ETag cache achieves same savings without server state. |

---

## 13. Recommended Direction

**Concise architectural recommendation (not an ADR — decision evidence):**

1. **Declare `eka context` (deterministic, bounded, reference-first) IS the Context Package. No second schema.** Add `objectHash` to `Entry`, document package shape.

2. **Enforce Progressive Disclosure as convention with minimal code:** add `--no-history` / `--only-section` filter flags (or `--level` sugar Later), default instruction `dependency --no-content` (L2) and expand lazily to full CKOs only for `role==constraint` or implementation focus.

3. **Place Stateful Delta in Agent Runtime as content-addressed cache keyed by `objectHash/snapshotDigest/fileHash`. Keep EKA stateless; expose ETags.**

4. **Implement Code Graph as derived Runtime cache (not CKO) — symbol + 1-hop import/caller, bounded 32/64, served via `code_context` tool mirroring `context` semantics. RAG/vector is optional tooling plugin, never canonical.**

5. **Fix Agent Configuration tax now (zero EKA core change):** freeze T0 to `router` only, enforce capability routing, trim SKILL.md to pointers, group MCP tools (read/write/ops), separate Instruction vs Knowledge (move excerpts to `std-/gls-` CKOs).

6. **Make Tool Output structured in Agent Runtime:** `ToolOutputProcessor` with per-tool extractors + pagination `hasMore` + lazy `read_output` — don't dump raw logs.

7. **Budget as Agent advisory (strata-priority), not EKA token gate.**

**Sequencing (MVP → Later):**

| Phase | Ships | Effort | Tokens reclaimed (qual) |
|-------|-------|--------|------------------------|
| **MVP (now)** | Skill pack audit (trim, pointers, T0 frozen), disclosure convention doc (`dependency --no-content` default + expand guide), `ToolOutputProcessor` for 5 tools, add `objectHash` to Entry, add `--no-history` filter | ~2 weeks, mostly docs + thin flags | HIGH fixed (20k) + MED per-task (5-10x for ref tasks) |
| **Next** | File map + import graph + symbol index (Go/TS/Python) + `code_context` tool (reuse context pattern) + Agent session cache (ETag, delta) | ~3 weeks | HIGH for code tasks (60-80% est) + HIGH for long sessions (40-70% repeats) |
| **Later** | ETag `ifNoneMatch` on MCP, `--max-units` with `truncated:true`, `--level` sugar, 1-hop caller/callee test linkage, MCP tool sharding, hierarchical view summaries (outside EKA) | ~2 weeks iter | MED (polish, edge cases) |
| **Never / Optional** | Vector primary, LLM summarization of CKOs, load-all skills, full call graph for context, server session | — | — |

---

## 14. Impact on Existing Architecture

| Impact class | What changes |
|-------------|-------------|
| **No change** | `eka-cko-v2`, R0-R13, strata/Domain/type, `P1-P16` principles, exchange RSF versioning, sync/snapshot/integrity, `eka get`/`view` contracts, MCP framing/hardening, EKA Standard corpus — all untouched. |
| **Minor update (docs + convention, no schema bump)** | Skill pack (`eka-knowledge-retrieval`, `eka-orientation`, `eka-engineering-workflow`) updated to: (a) enshrine `dependency --no-content` default + progressive disclosure, (b) instruction/knowledge separation (replace inline excerpts with `eka get` pointers), (c) T0 frozen prefix, router-gated. `INTRO.md` / `AGENTS.md` trimmed. |
| **Architectural enhancement (small code, backward compatible)** | `eka context`: add `objectHash` to `Entry` (additive field, no breaking), add `--no-history` / `--only-section=...` filters (additive flags). `eka get`: documented budget use via `--limit`. `eka-mcp`: grouping metadata (additive manifest change, old tool names preserved as alias if needed). |
| **New EKA capability required — derived (not canonical)** | `Code Graph` package (`eka-core/codegraph`): incremental file→symbol/import indexing, `code_context` & `code_get` MCP tools, derived cache at `~/.eka/cache/<project>/codegraph-<gitDigest>.json`. Not a new ADR domain — follow `contexts` pattern as Runtime consumer. Design record in `adr:` recommended, but not required to block MVP. |
| **New Agent Runtime capability required (not EKA)** | Agent Runtime owns: `ContextCache` (ETag, delta, revalidation head), `ContextBudget` (strata-priority), `ToolOutputProcessor` (per-tool extractors, pagination, lazy fetch), capability-routed skill loading (deterministic router), instruction tiering (T0/T1/T3). These are out of `eka-core`; they live in `eka-mcp` client side or agent `opencode` plugin layer. |
| **New ADR required?** | **Not for MVP.** MVP is convention + thin flags + pack audit, which fits ADR-014 (`contexts` as Runtime consumer) and P1/P4 without new ADR. **Later**: ADR for `Code Graph as derived Runtime cache (not CKO)` and `Context Efficiency: budget/state/cache/tiers` to canonize the patterns once validated by experiments. Do NOT author ADRs in this research step. |
| **EKA-MCP required?** | No breaking `eka-mcp` protocol change for MVP. Later: tool grouping (`eka_read/write/ops` aliasing) needs `manifest` version awareness + `configure --dry-run` preview truthfulness regression test. |

**Compatibility:** All MVP changes are additive (`--no-content` already exists, new flags are additive, skill trims are subtractive but non-breaking). No migration needed.

---

## 15. Research Gaps

`OPEN QUESTION` — cannot answer confidently without measurement or prototype:

1. **Numeric knee for "more context hurts"** — At what unit count (5? 12? 20?) does adding more CKOs degrade task success on engineering tasks? Needs task-based benchmark with token/quality dual metric (not generic summarization benchmarks).
2. **H4 progressive latency vs token trade-off** — Does 1-2 extra turns for expansion cost more in wall-clock / LLM calls than it saves in tokens? Needs `full upfront vs L2→expand` A/B on same tasks.
3. **Symbol vs file retrieval success** — For real `sto` implementation tasks across Go/TS/Python, what is precision @k for symbol+import vs file-level vs RAG chunk? Unproven reduction 60-80% needs data.
4. **Effective provider cache hit rate for EKA prefixes** — Does freezing T0 actually yield provider KV cache hits with `deepseek/opencode` via 9router? Vendor-specific; needs prompt caching docs + empirical header.
5. **Optimal code closure bounds (32 symbols / 64 units)** — Current 64 mirrors knowledge closure; code may need smaller or larger — needs graph density measurement per repo.
6. **Tool output gold standard** — What is "relevant evidence" per tool (e.g., `grep` relevance ranking function)? Needs labeled tool output / expected LLM action pairs.
7. **Stale cache risk tolerance** — How often does `lineForm latest` race with publish mid-session produce wrong implementation? Needs session trace analysis.
8. **Skill router accuracy** — Can deterministic keyword table achieve >95% routing accuracy without LLM fallback? Needs confusion matrix on historical tasks.

---

## 16. Suggested Experiments

Each experiment measures **both**: `tokens_input reduction` + `task outcome / reasoning quality` (human or rubric). Do NOT measure tokens alone. Small, isolated, non-invasive.

| # | Experiment | Variant A (control) | Variant B (treatment) | Measures | Gaps it closes |
|---|------------|---------------------|-----------------------|----------|----------------|
| E1 | **Full vs Minimal context** | `eka context --depth dependency` full (with focus content) | `dependency --no-content` (refs) + explicit `eka get` for focus only | Input tokens (A vs B), task success rubric (correct files touched + constraint adherence), LLM judge "did constraint get used?" | H1 knee + Finding 1 sufficiency |
| E2 | **File-level vs symbol-level code context** | Agent gets full file bodies for 5 top grep hits | Agent gets symbol slices + 1-hop imports for same 5 symbols, full fetch only on demand | Tokens, success on fix task, noise metric (irrelevant lines sent) | Gap 3, Finding 6 |
| E3 | **Static context vs progressive disclosure (+1 turn)** | One upfront `dependency` full (all neighbor content) | `L2 --no-content` then agent may expand 0-2 neighbors (capped at 2 fetches) | Tokens, turns, task success, p50 latency | H4 trade-off, Gap 2 |
| E4 | **Repeated context vs context delta (session)** | Agent resends full context each turn of a 4-turn ticket (`in-progress→note→in-review`) | Agent sends `etag` + `ifNoneMatch`; cache hit returns `notModified` | Repeated tokens, cache hit rate, stale read detection | H5 + Finding 5, Gap 7 |
| E5 | **Full tool output vs filtered evidence (ToolOutputProcessor)** | Raw `grep` (500 hits) + raw `test log` (200 lines) sent verbatim | `ToolOutputProcessor`: top 20 grep hits by proximity to focus file + structured test summary `{failed:2, firstStacktrace:…}` + `hasMore:true` handle | Tokens, agent correctly identifies failing test, false-negative rate | Gap 6, Finding 7 |
| E6 | **All skills vs routed skills** | Agent loads all 11 EKA skills at session start | Agent loads only `eka-router` + routes 1-2 skills on demand | Prefix tokens, task success per task type (should be equal), router mis-classify rate | Gap 8, H6 |
| E7 | **KG deterministic vs RAG retrieval (code discovery fallback)** | `symbol search` only (symbol+import) for "where is auth logic?" | `symbol search` + `semantic RAG` optional `code_search` ranked merge | Recall @k, useful ref rate, hallucination rate, determinism audit | H3 partial fail zone, Gap 3 |
| E8 | **Strata-priority truncation vs tail truncation (budget)** | When over budget, naively truncate tail of context | Strata-priority keep (constraints > dependencies > downstream noise), truncate rest + `truncated:true` flag | Task success when budget forced (e.g., 16k cap), constraint retention rate | Finding 4, Gap 1 |

**Experiment hygiene:**
- Use real `eka-cli` store fixtures (`testdata/conformance/valid`, `sync/valid`, cloned repos with known tasks like `sto-login-email`).
- Record both `tokens_transmitted` and `tokens_prefix_cached` (provider header if available) separately — don't conflate #2 vs #3 vs #4 of §11.
- Human rubric for engineering quality: `correct constraint cited`, `correct files touched`, `state transition valid`, `no hallucinated requirement`.
- Report as `FACT/HYPOTHESIS` with raw counts, not invented benchmarks.

---

## Appendix — Terminology Alignment (for implementers)

| Research doc term | Current EKA term | Notes |
|-------------------|------------------|-------|
| Context Package | Context Object `eka-context-v1` | Same thing; don't create second schema |
| L0-L6 | `local / dependency / engineering` + filters (`--no-content`, `--no-history`, `--only-section`) | L-system is alias over existing |
| Relevance Score | `role`, `stratum`, `domain`, `type` (deterministic classifiers) | No numeric score |
| Context Budget | `summary.units` + `--limit/offset` + closure 64 cap | Unit/byte budget at EKA, token budget at Agent |
| Context State / Delta | `objectHash`, `snapshotDigest`, `fileHash` + `Timeline.Line` head check + client `ETag` cache | EKA stateless, Agent stateful |
| Code Graph | Derived Runtime cache `codegraph-<gitDigest>.json` (future) | Not a CKO |
| Tool Output Evidence | `ToolOutputProcessor` structured `{evidence, hasMore, fullRef}` | Agent Runtime |

---

## References

- `EKA` summary file (Standard 1.1), `eka-core/contexts/engine.go`, `contexts/object.go`, `cmd/context.go`, `cmd/get.go`, `cmd/context_test.go` golden, `eka-mcp` skill pack & `README.md`, `anvil.yaml`, `eka.yaml`.
- Stratum Authority Invariant, P1/P4/P5/P8/P14/P16, R0-R13 as in `EKA` summary.
- Agent config measurement: `~/.config/opencode/skills` (809KB total, EKA 103KB), `opencode.json` (`tool_output.max_bytes 8192`), `mappings/*.toml`.

> End of Research Finding. Next step is **review** (not implementation). If validated, MVP items should be expressed as `spk-` (spike validation) or `sto-` tasks with explicit experiments E1-E8, not as assumptions baked into ADRs.

