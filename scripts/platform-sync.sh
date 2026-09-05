#!/bin/sh
# EKA platform_sync — Anvil lifecycle phase: symlink Layer-2 git hooks
# ADR-035 v3 + spec provenance-capture:1 — Layer 2 Git Hook Terdistribusi via eka-standard + anvil lifecycle
# Installed via `anvil install` / `anvil deployment install` platform_sync phase
# Also runnable standalone: ./scripts/platform-sync.sh  or  eka capture --install-hooks
set -eu

# Resolve repo top (supports running from any subdir)
if GIT_TOP="$(git rev-parse --show-toplevel 2>/dev/null)"; then
  REPO_ROOT="$GIT_TOP"
else
  REPO_ROOT="$(pwd)"
fi

# Template source: prefer vendored eka-standard/templates/hooks, fallback to embedded path
TEMPLATE_SRC=""
for CANDIDATE in \
  "$REPO_ROOT/eka-standard/templates/hooks" \
  "/home/m2codeloan/m2code/maleolabs/eka/eka-standard/templates/hooks" \
  "$(dirname "$0")/../eka-standard/templates/hooks" \
  "./templates/hooks"
do
  if [ -d "$CANDIDATE" ]; then
    TEMPLATE_SRC="$CANDIDATE"
    break
  fi
done

# When run from eka-cli repo, templates live in sibling eka-standard
if [ -z "$TEMPLATE_SRC" ] && [ -d "$(dirname "$0")/../../eka-standard/templates/hooks" ]; then
  TEMPLATE_SRC="$(dirname "$0")/../../eka-standard/templates/hooks"
fi

if [ -z "$TEMPLATE_SRC" ] || [ ! -d "$TEMPLATE_SRC" ]; then
  # Fallback: eka binary embeds hooks? generate minimal hooks inline
  echo "[platform_sync] template source not found — generating inline hooks" >&2
  mkdir -p "$REPO_ROOT/.git/hooks"
  cat > "$REPO_ROOT/.git/hooks/pre-commit" <<'HOOK'
#!/bin/sh
command -v eka >/dev/null 2>&1 || exit 0
[ -f eka.yaml ] || exit 0
if git diff --cached --quiet 2>/dev/null; then exit 0; fi
echo "[eka-capture] staged changes — run 'eka capture --reconcile --from-git'" >&2 || true
exit 0
HOOK
  cat > "$REPO_ROOT/.git/hooks/pre-push" <<'HOOK'
#!/bin/sh
command -v eka >/dev/null 2>&1 || exit 0
[ -f eka.yaml ] || exit 0
eka capture --reconcile --from-git >/dev/null 2>&1 || true
exit 0
HOOK
  chmod +x "$REPO_ROOT/.git/hooks/pre-commit" "$REPO_ROOT/.git/hooks/pre-push"
  echo "[platform_sync] inline hooks installed to $REPO_ROOT/.git/hooks" >&2
  exit 0
fi

# Ensure .git/hooks exists
mkdir -p "$REPO_ROOT/.git/hooks"

installed=0
for hook in pre-commit pre-push; do
  SRC="$TEMPLATE_SRC/$hook"
  DST="$REPO_ROOT/.git/hooks/$hook"
  if [ ! -f "$SRC" ]; then
    echo "[platform_sync] skip $hook: source missing $SRC" >&2
    continue
  fi
  # Symlink (preferred) — preserves updates from template source
  # Fallback to copy if symlink not supported or cross-device
  if ln -sf "$SRC" "$DST" 2>/dev/null; then
    chmod +x "$DST" 2>/dev/null || true
    echo "[platform_sync] symlinked $hook -> $SRC" >&2
  else
    cp -f "$SRC" "$DST"
    chmod +x "$DST"
    echo "[platform_sync] copied $hook -> $DST" >&2
  fi
  installed=$((installed + 1))
done

if [ "$installed" -eq 0 ]; then
  echo "[platform_sync] warning: no hooks installed (no templates found)" >&2
  exit 0
fi

echo "[platform_sync] done: $installed hook(s) installed from $TEMPLATE_SRC" >&2
