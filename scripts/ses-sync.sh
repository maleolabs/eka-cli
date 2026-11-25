#!/bin/sh
# ses-sync.sh — dual-write .eka/execution-state.md ↔ eka/ses:execution-state
# Acceptance:
# - inside repo: publish ses:execution-state after each checkpoint (immutable instanceVersion)
# - outside repo: fallback file-only
# - retry on conflict (expectedRevision mismatch)
set -eu
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ ! -f "$REPO_ROOT/eka.yaml" ]; then
  exit 0
fi
CHECKPOINT="$REPO_ROOT/.eka/execution-state.md"
if [ ! -f "$CHECKPOINT" ]; then
  exit 0
fi
# Derive project/namespace from the repo's eka.yaml so the ses line is
# attributed to the current project (project-scoped resolution in
# `eka status` reads project-local ses first). Fallback to eka/eka
# when the fields are absent.
PROJ="$(grep -E '^[[:space:]]*project:' "$REPO_ROOT/eka.yaml" | head -1 | sed 's/.*project:[[:space:]]*//;s/[[:space:]#].*//')"
NS="$(grep -E '^[[:space:]]*namespace:' "$REPO_ROOT/eka.yaml" | head -1 | sed 's/.*namespace:[[:space:]]*//;s/[[:space:]#].*//')"
PROJ="${PROJ:-eka}"
NS="${NS:-eka}"
# Try publish, retry once on failure (conflict)
set +e
eka new "$NS/ses:execution-state" --project "$PROJ" --namespace "$NS" >/dev/null 2>&1 || true
# Minimal content update: ensure required R9 keys exist via python
PROJ="$PROJ" NS="$NS" python3 <<'PY' >/dev/null 2>&1 || true
import json, os, pathlib
proj = os.environ.get("PROJ", "eka")
ns = os.environ.get("NS", "eka")
p = pathlib.Path.home() / f".eka/drafts/{proj}/ses-execution-state.json"
if p.exists():
    d = json.loads(p.read_text())
    d["content"].setdefault("context", "dual-write sync")
    d["content"].setdefault("verification", f"eka get {ns}/ses:execution-state")
    d["content"].setdefault("scope", "full")
    d["content"].setdefault("mode", "solo")
    p.write_text(json.dumps(d, indent=2))
PY
eka publish "$NS/ses:execution-state" >/dev/null 2>&1
RC=$?
if [ $RC -ne 0 ]; then
  sleep 1
  eka publish "$NS/ses:execution-state" >/dev/null 2>&1 || true
fi
exit 0
