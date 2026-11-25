#!/bin/sh
# ses-init-migrate.sh — one-time migrasi .eka/execution-state.md -> eka/ses:execution-state:1
# Idempotent: second run no-op if ses already exists
set -eu
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ ! -f "$REPO_ROOT/eka.yaml" ]; then
  exit 0
fi
PROJ="$(grep -E '^[[:space:]]*project:' "$REPO_ROOT/eka.yaml" | head -1 | sed 's/.*project:[[:space:]]*//;s/[[:space:]#].*//')"
NS="$(grep -E '^[[:space:]]*namespace:' "$REPO_ROOT/eka.yaml" | head -1 | sed 's/.*namespace:[[:space:]]*//;s/[[:space:]#].*//')"
PROJ="${PROJ:-eka}"
NS="${NS:-eka}"
if eka get "$NS/ses:execution-state" >/dev/null 2>&1; then
  # ses already exists — idempotent no-op
  exit 0
fi
CHECKPOINT="$REPO_ROOT/.eka/execution-state.md"
if [ ! -f "$CHECKPOINT" ]; then
  exit 0
fi
# Scaffold and publish ses:1 from checkpoint
eka new "$NS/ses:execution-state" --project "$PROJ" --namespace "$NS" >/dev/null 2>&1 || true
PROJ="$PROJ" NS="$NS" python3 <<'PY' >/dev/null 2>&1 || true
import json, os, pathlib
proj = os.environ.get("PROJ", "eka")
ns = os.environ.get("NS", "eka")
p = pathlib.Path.home() / f".eka/drafts/{proj}/ses-execution-state.json"
if p.exists():
    d = json.loads(p.read_text())
    d["content"]["context"] = "init migration from .eka/execution-state.md"
    d["content"]["verification"] = f"eka get {ns}/ses:execution-state"
    d["content"].setdefault("scope", "full")
    d["content"].setdefault("mode", "solo")
    p.write_text(json.dumps(d, indent=2))
PY
eka publish "$NS/ses:execution-state" >/dev/null 2>&1 || true
# verify sync push will transport
eka sync push >/dev/null 2>&1 || true
