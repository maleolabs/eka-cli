# ADR-0XX — Freshness (Validity Horizon) for Canonical Knowledge Objects

- **Status:** Proposed — runtime format for review; not yet registered as a CKO. When registered,
  publish as `eka/adr:<id>` with `revision: 1`, `contentState: proposed`, and
  `dimensionsSecondary: [architecture]`.
- **Date:** 2026-11-18
- **Supersedes / amends:** none. Additive extension of `eka-cko-v2` and the R1–R13 rule set.
- **Related precedent:** ADR-035 (inferred direct task capture) — same additive envelope-extension
  shape: `omitempty` field on frontmatter + `exchange.Unit` + `machine.Document`, allowlist entry in
  the JSON adapter, no migration of existing corpora.

---

## Context

EKA specifies and implements provenance (`provenance`, `sourcePromptHash`, `confidence`,
`captureMeta`): the model records *who* produced knowledge and *how confidently* — but never *until
when* it is trustworthy. The only temporal field, `updated`, is the file modification time
(`runtime/draft.go:506` derives it from `os.Stat().ModTime()`), which answers when the file was
touched, not whether its content still holds. Nothing in the corpus can express "this ADR describes
a component that no longer exists", and no query answers "is this artifact stale?" or "which
artifacts in this Engineering Domain have outlived their stated validity?"

The damage is asymmetric by stratum. Because lower strata may not contradict higher ones, a stale
stratum-2 artifact is the most damaging kind of wrongness: it does not merely misinform, it
*forbids correction* with institutional authority. An agent reading `eka get` receives a
confident-looking document whose `stateVector.contentState` says `accepted` and whose only date
signal says "touched last Tuesday". The missing axis is not accuracy (provenance covers that) and not
completeness (conformance covers that) — it is **decay**.

This ADR therefore adds exactly one optional envelope field plus one explicit confirmation
mechanism, and deliberately stops short of a freshness *engine*.

---

## Decision

### 1. Where the horizon lives — **(a) a new envelope field, adjacent to `Provenance`**

`validUntil` (a date) is a field on the CKO envelope: `exchange.Unit` (`exchange/model.go`),
`machine.Document` (`machine/document.go`), and the artifact model (`conformance.Artifact`,
`conformance/artifact.go`), sourced from the artifact's own top-level authoring field. Rejected:
`Classification` (classification answers *what kind of knowledge is this*; a validity horizon is
*lifetime metadata*, and `Classification` is a taxonomy vocabulary that rules R6 validates
against closed token sets — folding a date into it forces R6 to grow a second, incompatible
validation mode); frontmatter-only with no envelope projection (the machine envelope **is** the
agent-facing contract — `machine/document.go` states it is the canonical JSON consumed by `eka get`,
MCP and Atrium; a horizon invisible to `eka get` is a horizon no agent will ever obey, which
repeatsthe exact failure this ADR exists to prevent); a companion artifact type (`vld:<id>`)
(a separate CKO per assertion means N extra objects, a new token family, a new rule, and a
join at query time — for one date or one date-range).

Field placement mirrors `Provenance` exactly: top-level, camelCase, `omitempty`, declared in the
envelope struct with a fixed order so the serializer stays deterministic. Envelope, not content,
because the horizon is metadata *about* the payload, and because R9 treats content keys as the type's
declared section vocabulary.

**Accepted cost:** the horizon is envelope state, so changing it is a content change requiring a new
revision — that is constraint 4, not a defect.

### 2. The value's shape — **a date, not a duration**

`validUntil: "YYYY-MM-DD"`. Absolute, not relative.

The tempting alternative is a duration (`validFor: 12mo`) re-evaluated from a confirmation date, and
it fails on the failure mode that matters: **silent, unbounded staleness**. If `validFor` is
re-evaluated against `updated` (file mtime), every `touch`, formatter run, or rebase resets the clock
and the horizon becomes a lie that refreshes itself. If it is re-evaluated against `created`, it
decays correctly but invisibly — a corpus of 500 artifacts all crosses the same threshold on the same
day, and the operator learns about it by waking up to a wall of stale objects. A date is
*self-documenting*: the author who writes `validUntil: 2027-01-01` has stated a belief about the
world, and reading it aloud in a diff is a real review signal (the same reason `changeLog.date` is a
date and not an age).

Maintainability is handled by the **confirmation** side, not by making the value relative:
`lastConfirmed` (see §6/§1 mechanics below) records when a human last asserted the content still
holds, and the freshness query reports `validUntil` **and** `lastConfirmed` **and**
`daysUntilExpiry`. The reviewer sees absolute commitments with absolute evidence, which is the only
form that survives five years of maintainer turnover.

Mechanically, the pair is the answer to "who resets the clock":

- `validUntil` — the declared horizon. Consumed by the freshness query. Never written by tooling.
- `lastConfirmed` — set by a human/agent action (`eka publish` with an explicit flag, or an explicit
  revision). Reported alongside, never used to *derive* `validUntil`.

Normalising the two into one derived field was rejected: a derived horizon cannot tell the difference
between "re-affirmed unchanged" and "re-published because someone fixed a typo in an unrelated
section", and manufacturing that inference is precisely the "false confidence in both directions"
the brief warns against.

### 3. Who sets it — **the author, by hand; per-type defaults deferred, not shipped**

No default. An artifact whose author declared no horizon is **not stale** and **not fresh** — it is
**undeclared** (`unknown`), a third state that the query reports explicitly and a rule never penalises.

Reasoning: a default is a claim, and this model has no basis for the claim. Compressing "we know this
will be re-checked Q2" into a type-level constant forces ADRs (median lifetime: years) and `tkt-`
items (median lifetime: days) — which are *not* the same kind of object, and not the same kind of
rule anyway: freshness is a property of *stratum*, not of token, since a stratum-2 `sto` and a
stratum-3 `tkt` have entirely different decay rates. A default per type would also regenerate the
`updated`-derived illusion discussed in §2: it looks like a signal, it is a guess, and a wrong guess
is *worse than silence* because silence is honestly uninformative. If a future ADR introduces
defaults, it must do so per *type and dimension*, must be overridable per artifact, and must render
"defaulted" distinguishably from "declared" in every query.

What tooling *may* do (deferred, see "What NOT to build"): scaffold an empty `validUntil:` line in
`draftDoc` so the field is visible at authoring time. Empty is not a default.

`lastConfirmed` is likewise author-set. It is **not** written automatically on publish — an
automatic write would turn the field into `updated` with a nicer name.

### 4. Advisory or blocking — **advisory only; conformance never compares to "now"**

A stale artifact never fails validation. The horizon's *shape* is a structural concern (a malformed
date is an R0 structural finding, exactly like a malformed `created`), but its *value* is never
compared against the current clock inside the rule engine.

Three reasons:

1. **Precedent.** `conformance/report.go:22` documents `SeverityWarning` as "never blocks: the
   repository may be committed with …" — refusal is reserved for structural violation. Freshness
   expiry is editorial judgement about the world, not a structural defect. An ADR that is
   *correct but old* is a conformant ADR.
2. **Determinism.** A blocking rule makes the verdict a function of wall-clock time. The same
   commit would pass on Monday and fail in March with no repository change. That destroys
   reproducible conformance (and, incidentally, reproducibility of snapshots and CI), and it breaks
   constraint 2 in spirit: conformance must not depend on mutable external state.
3. **Usability.** Every long-lived corpus crosses every horizon eventually. A blocking rule means
   the standard refuses to run on a healthy, well-maintained repository whose oldest ADR is simply
   *old*.

So: **freshness is a question, not a verdict.** It surfaces in a query surface (§5) that an agent or
operator inspects, and — separately and optionally — as a non-blocking `SeverityWarning` finding in a
*scheduled* CI job, never in the publish gate.

Symmetrically: an artifact with `validUntil` far in the future is **not** less conformant. Freshness
cannot become a grading axis codifying "good artefacts have long horizons", or every author will
write `9999-12-31`.

### 5. The query surface — **a flag on the existing retrieval surface plus `eka status`; no new verb**

The bypass is one line: **`eka get <ref>` already emits the field**, because the envelope carries it.
An agent doing `eka get eka/adr:035` sees `"validUntil": "2027-01-01"` with zero new machinery. That
alone satisfies constraint 5 for the single-artifact case and is the highest-leverage change in this
ADR.

For the two questions that need a clock:

- **`eka get <ref> --freshness`** — adds `freshness: {validUntil, lastConfirmed, status}` to the
  existing document, where `status ∈ {undeclared, fresh, stale}`. This is the established
  retrieval-option pattern (`--upstream`, `--downstream`, `--timeline` appended at the end of the
  schema, absent by default, byte-identical without the flag) documented in
  `machine/document.go:61-67` and `cmd/get.go:405-409`. Cost: one `Document` field + one flag, and
  the default document stays byte-identical.
- **`eka status`** — one summary line, `Stale knowledge: N (of M bounded)`, with `--json`
  including the list. `status` is already the deterministic read-only probe with a JSON schema
  (`schema status-v1`) that operators and CI already consume; stale counts belong in an overview, not
  in a *new verb* whose whole surface would be one number.

Rejected: `eka freshness` as a verb — a new command is a new help entry, a new group assignment
(`help_groups.go`), a new exit-code contract, and a new thing agents must be told about; every
discovery path we have is worse than "it's a flag on the command they already run". Rejected:
`eka integrity check` — that command's contract is *content-derived immutability* (payload hashes,
reference targets, index columns), and expiry is neither content-derived nor tamper evidence; putting
a clock inside a command whose exit code means "your store is corrupt" would make a stale ADR look
like a damaged store. Rejected: `eka context`/`eka view` — projections for human/agent understanding,
not for machine-consumed staleness.

Domain-level staleness is the same query with the existing domain filters:
`eka get --type adr --dimension architecture --freshness` (the `--freshness` flag applies to the
domain form too). No separate domain query is designed.

### 6. Evidence beyond the declared horizon — **out of scope for v1**

There is no deterministic corpus-side evidence of staleness in EKA today, and inventing one would be
fiction:

- The `file:` evidence-path idea does not exist in the model. Reference strings follow the Rule 5
  grammar `<namespace>/<type>:<id>[:<version>]` (`rules.go:201-212`) and `file:` is not a type token,
  so an artifact cannot legally carry a path reference today. Adding one is a new reference grammar
  plus a new graph check plus a filesystem-coupling to conformance — none of which is a freshness
  concern.
- **Referenced-artifact evidence already exists and is already free**: freshness of a target is
  *observable by following an existing reference*. `eka get X --upstream --freshness` places the
  stale upstream next to the artifact that depends on it. That answers "my foundation moved" without
  a single new rule, new field, or new resolution path. It is the only cross-artifact decay signal
  this model can honestly produce, because in EKA "the subject moved" *is* "the CKO it was about was
  superseded".
- Where that cheap signal becomes a *deterministic stale marker* is a future *derivation policy*,
  not evidence: e.g. "an artifact whose `derives-from`/`refines` target is retired is `stale`". That
  is a graph operation over immutable facts and is out of scope here, for one reason: v1 must not
  define a second, implicit clock (staleness-by-inheritance) before the explicit one has been used
  for a while. Add it only when the freshness query sees adoption.

The proposed separation is therefore: **freshness = the declared horizon (+ references traversed on
demand)**; **integrity = immutability and resolution**, unchanged. If a filesystem-evidence
mechanism ever ships, it belongs to neither: it belongs to a new *evidence* concern with its own
deterministic inputs, and it must not enter `integrity check`.

---

## Consequences

**Becomes possible**

- An agent reading `eka get` sees a horizon without being told the field exists. `--freshness`
  turns it into a verdict when the agent has a clock.
- Operators get a domain-scoped count of knowledge past its horizon from the command they already
  run, JSON-included.
- Stale stratum-2 artifacts become *nameable* without being *refused* — the mechanism is a query,
  which is the only mechanism that survives contact with a five-year corpus.
- Horizons follow the existing immutable-revision discipline: a changed horizon is a new revision
  with a `contentState` change-log entry like any other, so the model history records *when the
  author believed what*.

**Becomes harder**

- Two more fields to keep coherent across seven layers: frontmatter/Markdown adapter, JSON adapter
  allowlist (`conformance/artifact.go:241-263`), `conformance.Artifact`, `exchange.Unit`,
  `machine.Document`, `runtime` draft/inline builders, and (eventually) the spec. This is the price
  of the envelope placement and of ADR-035's precedent: every envelope field is seven edits.
- `lastConfirmed` is a second temporal field that authors must understand is *not* `updated`. The
  doc comment must say so, and the freshness query must print both, or the distinction will be
  silently collapsed.
- Persistent pressure to add "defaults" and "derived" freshness. The ADR must be cited to resist it.

**Must be maintained**

- The `omitempty` + fixed-field-order contract. Adding fields in declaration order, never
  reordering, or the deterministic serializer breaks.
- The structural validation of the value (a malformed `validUntil` is R0, the same class as
  `created`) — cheap, and it stops typos from silently becoming "undeclared".
- Documentation that expiry is advisory. If it is ever read as a verdict, this ADR is dead.

---

## Alternatives (rejected, with the specific reason)

| Option | Rejected because |
|---|---|
| Horizon in `Classification` | `Classification` is the taxonomy vocabulary R6 validates against closed token sets; a date there needs a second validation mode in R6 and confuses "what kind of knowledge" with "how long it lives". |
| Horizon in frontmatter only, not projected | The envelope **is** the agent-facing contract. An invisible horizon is an unenforceable horizon — the exact failure the ADR exists to fix. |
| Horizon as a companion CKO (`vld:` type) | New token family, new rule, N extra objects, a join at query time — for one date. Violates "one dependency/abstraction is a liability". |
| Duration (`validFor: 12mo`) re-evaluated against `updated` | `updated` is file mtime (`draft.go:506`). Every touch resets the clock: silent, unbounded false freshness. |
| Duration re-evaluated against `created` | Correct decay, invisible decay. Whole corpus expires simultaneously; operator blindness. |
| Default horizon per artifact type | A guess presented as knowledge. Manufactures false confidence in both directions; a wrong default is worse than unambiguous `undeclared`. |
| Auto-derive the horizon from `changeLog` / publish count | A derived field cannot tell a re-commitment from an unrelated edit; derives certainty the model does not have. |
| Blocking validation on expiry | Verdict becomes a function of wall-clock time (breaks reproducible conformance), contradicts the documented `SeverityWarning` precedent, and breaks every long-lived corpus eventually. |
| A new `eka freshness` verb | New help/group/exit-code surface for one number; existing flag pattern (`--upstream`) covers it at a fraction of the cost. |
| Freshness inside `eka integrity check` | `integrity check` means "content-derived immutability is violated" with exit 1. A stale ADR is not a damaged store. |
| `file:` evidence paths for staleness | No such grammar exists (`rules.go:201`); adding one means a new reference grammar, a new graph pass, and filesystem coupling inside conformance. Evidence, not freshness. |
| Staleness-by-inheritance in v1 (`retired` target ⇒ stale) | A second implicit clock before the explicit one has been used. Valuable, but this v1 must first prove anyone reads the explicit horizon. |

---

## Minimal implementation surface

Smallest change satisfying the six questions, in dependency order. Sizes: **S** ≈ ≤50 lines,
**M** ≈ 50–200 lines, **L** > 200 lines. No file listed needs >M.

| # | File | Change | Size |
|---|---|---|---|
| 1 | `eka-core/conformance/artifact.go` | Add `ValidUntil string` + `LastConfirmed string` to `Artifact` struct (near `Created`/`Updated`, line ~55). In `classifyMap` (line ~621), parse `validUntil` / `lastConfirmed` with the existing `validDate` helper; malformed → `RuleStructural` finding (same shape as `created` at line ~730). | **S** |
| 2 | `eka-core/conformance/artifact.go` | Add `"validUntil": true, "lastConfirmed": true` to `allowedJSONTopLevel` (line ~241). **Without this every JSON-native artifact carrying the field is an R0 structural error.** | **S** |
| 3 | `eka-core/exchange/model.go` | Add `ValidUntil string \`json:"validUntil,omitempty"\`` + `LastConfirmed string \`json:"lastConfirmed,omitempty"\`` to `Unit`, immediately after `CaptureMeta` (line ~263), preserving declaration order. | **S** |
| 4 | `eka-core/exchange/artifact.go` | In `(*Unit).ToArtifact`, carry the two fields from `u` to `a`. **This is the read-side gap ADR-035 shipped with:** `builder.go:toUnit` and `runtime/draft.go:~991` already populate `u.Provenance` but `ToArtifact` never copied it back to `a.Provenance` — do not repeat that. Add both directions now. | **S** |
| 5 | `eka-core/exchange/builder.go` | In `toUnit` (line ~335), set `ValidUntil: a.ValidUntil, LastConfirmed: a.LastConfirmed`. | **S** |
| 6 | `eka-core/machine/document.go` | Add `ValidUntil string` + `LastConfirmed string` (`omitempty`) to `Document` after `CaptureMeta` (line ~56); map them in `NewDocument` (line ~191); add `Freshness *Freshness \`json:"freshness,omitempty"\`` **at the end**, after `Timeline`, per the retrieval-option contract (line ~61). New type `Freshness{ValidUntil, LastConfirmed, DaysUntilExpiry, Status}`. `NewDocument` never sets `Freshness`. | **M** |
| 7 | `eka-core/runtime/freshness.go` (new) | One function: `FreshnessFor(u *exchange.Unit, now time.Time) machine.Freshness` — pure, deterministic, `now` injected (never `time.Now()` inside). `now.IsZero()` ⇒ `status: "undeclared"` path avoids a hidden clock in tests. | **S** |
| 8 | `eka-core/runtime/draft.go` | `inlineCKO` (line ~1149): add `ValidUntil`/`LastConfirmed` and thread them into the `exchange.Unit` built at line ~974. Optionally scaffold empty `validUntil:` in `draftDoc` (line ~538) — visible, empty, **not** a default. | **S** |
| 9 | `eka-cli/cmd/get.go` | One flag `--freshness` (pattern: `flagGetUpstream`, line ~407) → calls #7 for the identity form and for each document in the domain form. | **S–M** |
| 10 | `eka-cli/cmd/status.go` | One summary line + `--json` field: count of units with `status == stale` and total bounded. Needs the store to expose a `Units(...)` scan already used at `status.go:128`. | **S–M** |
| 11 | `eka-core/conformance/rules.go` | **Optional, non-blocking.** A `SeverityWarning` finding when `validUntil` is in the past — only if a clock is passed in explicitly via `Options` and only surfaced in a scheduled job. Skip in v1 unless the query surface stalls. | **S** |
| 12 | `eka-standard/specification/` | New `tokens/…`/`envelope` prose + a `1.4` CHANGELOG entry (additive). Normative wording defers to this ADR. | **M** |

Total: ~11 files, all small to medium; **zero migrations**, **zero changes to existing rules' verdicts**,
**byte-identical default documents** (all fields `omitempty`).

**Ordering matters:** 1–2 land together (a field the JSON adapter rejects is worse than no field),
then 3–5 (internal plumbing), then 6 (the contract), then 7–10 (the surfaces), then 12 (the
standard). Steps 1–6 are the whole model change and are testable by asserting the default document
is byte-identical and that a `validUntil` round-trips.

---

## What NOT to build

Explicitly out of scope for v1. Each is real; each is someone else's ADR:

1. **A blocking rule on expiry.** Covered in §4. If freshness ever blocks, it must be a CI-scheduled
   *gate* with an explicit `--now`, never a conformance verdict.
2. **Per-type or per-dimension default horizons.** §3. Only after observed usage shows which
   defaults people actually accept — and then they must render as "defaulted", not "declared".
3. **Automatic `lastConfirmed` on publish.** Turns the field into `updated` with a new name.
4. **`file:` evidence paths, dead-reference detection, or any filesystem-coupled freshness check.**
   §6. Wrong concern, and no grammar exists to hang it on.
5. **Staleness-by-inheritance** (`retired upstream ⇒ stale downstream`) in the freshness *verdict*.
   Ships free as `--upstream --freshness`; a derived verdict can wait.
6. **A duration/relative-horizon syntax** (`validFor`, `renewEvery`). §2. The silent-refresh failure
   mode is the whole reason this ADR picked dates.
7. **A new CLI verb, a new exit code, or a new report file.** §5.
8. **Freshness in `eka integrity check`.** §5.
9. **Any change to `updated`, `created`, or `changeLog` semantics.** `updated` stays file mtime;
   renaming or redefining it is a 2.0 break, not an additive change.
10. **A freshness score, grade, or "knowledge health" metric.** §4 last paragraph — a grade invites
    `9999-12-31` and destroys the signal.

---

## Open issues / risks

- **Adoption risk (the real one).** An optional advisory field that nobody writes is inert. Mitigation
  is the free `eka get` visibility (§5) and nothing more — if the field needs a nagging mechanism,
  the design is wrong. Measure adoption before designing v2.
- **`lastConfirmed` vs `updated` ambiguity.** Highest documentation risk in this ADR. Two temporal
  fields must never be described as similar; the query must always print both.
- **Seven-layer field cost.** Any future envelope field repeats it. Worth noting in the standard that
  envelope extension is a *deliberate* seven-edit act, not the AEP default — content-layer fields
  (schema-free sidecar) may be the better default for future additive metadata.
- **`ToArtifact` asymmetry precedent.** ADR-035 shipped `Provenance` into `Unit` but `ToArtifact`
  never restored `a.Provenance`, so CKO-level validation observes the default `human` for every
  stored object. Not fixed here (unrelated scope, and it may be intentional: `ValidateCKO` consumes
  `exchange.Unit`). Flag it to the ADR-035 owner; do not silently "fix" it in this ADR's diff.
