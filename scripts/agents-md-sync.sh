#!/bin/sh
# agents-md-sync.sh — auto append EKA workflow block to AGENTS.md idempotent
# Marker: <!-- eka:workflow:start --> ... <!-- eka:workflow:end -->
set -eu
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
AGENTS="$REPO_ROOT/AGENTS.md"
TEMPLATE_MARKER_START="<!-- eka:workflow:start -->"
TEMPLATE_MARKER_END="<!-- eka:workflow:end -->"
if [ ! -f "$REPO_ROOT/eka.yaml" ]; then
  exit 0
fi
PROJECT=$(grep '^project:' "$REPO_ROOT/eka.yaml" | awk '{print $2}' | tr -d '"' || echo "eka")
NAMESPACE=$(grep '^namespace:' "$REPO_ROOT/eka.yaml" | awk '{print $2}' | tr -d '"' || echo "eka")
CTR=$(eka get containers 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); act=[c for c in d.get('containers',[]) if c['containerState']=='active']; print(act[0]['canonicalForm'] if act else 'none')" 2>/dev/null || echo "none")
BLOCK="$TEMPLATE_MARKER_START
## EKA Workflow Context (auto-managed, do not edit between markers)
Project: $PROJECT | Namespace: $NAMESPACE | Active ctr: $CTR
Commands: eka get <form>, eka context <subject>, eka transition <target> <to>, eka new/publish, eka sync push
Skills: eka-orientation, eka-knowledge-retrieval, eka-knowledge-authoring, eka-engineering-workflow
$TEMPLATE_MARKER_END"
if [ ! -f "$AGENTS" ]; then
  printf "%s\n" "$BLOCK" > "$AGENTS"
  exit 0
fi
if grep -q "eka:workflow:start" "$AGENTS"; then
  # replace existing block idempotently
  python3 <<PY
import pathlib
p = pathlib.Path("$AGENTS")
t = p.read_text()
start = """$TEMPLATE_MARKER_START"""
end = """$TEMPLATE_MARKER_END"""
block = """$BLOCK"""
import re
pattern = re.compile(re.escape(start) + r".*?" + re.escape(end), re.DOTALL)
if pattern.search(t):
    t = pattern.sub(block, t)
else:
    t = t.rstrip() + "\n\n" + block + "\n"
p.write_text(t)
print("replaced")
PY
else
  printf "\n%s\n" "$BLOCK" >> "$AGENTS"
fi
# pre-commit warn if missing (hook will call this)
exit 0
