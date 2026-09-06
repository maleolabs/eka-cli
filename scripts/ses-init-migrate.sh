#!/bin/sh
# ses-init-migrate.sh — one-time migrasi .eka/execution-state.md -> eka/ses:execution-state:1
# Idempotent: second run no-op if ses already exists
set -eu
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ ! -f "$REPO_ROOT/eka.yaml" ]; then
  exit 0
fi
if eka get eka/ses:execution-state >/dev/null 2>&1; then
  # ses already exists — idempotent no-op
  exit 0
fi
CHECKPOINT="$REPO_ROOT/.eka/execution-state.md"
if [ ! -f "$CHECKPOINT" ]; then
  exit 0
fi
# Scaffold and publish ses:1 from checkpoint
eka new eka/ses:execution-state --project eka --namespace eka >/dev/null 2>&1 || true
python3 <<'PY' >/dev/null 2>&1 || true
import json, pathlib
p = pathlib.Path.home() / ".eka/drafts/eka/ses-execution-state.json"
if p.exists():
    d = json.loads(p.read_text())
    d["content"]["context"] = "init migration from .eka/execution-state.md"
    d["content"]["verification"] = "eka get eka/ses:execution-state"
    d["content"].setdefault("scope", "full")
    d["content"].setdefault("mode", "solo")
    p.write_text(json.dumps(d, indent=2))
PY
eka publish eka/ses:execution-state >/dev/null 2>&1 || true
# verify sync push will transport
eka sync push >/dev/null 2>&1 || true
