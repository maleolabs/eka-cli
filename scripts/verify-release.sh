#!/bin/sh
# verify-release.sh — the pre-tag gate over the version axes the EKA
# repositories share. Read-only: never writes, tags, commits or pushes.
#
# Why: a half-done release is green from inside any single repository. Both
# failures that actually occurred — eka-core tagged while a go.mod pin still
# named the previous version, and the standard's corpus version disagreeing
# with its own declaration — were invisible from one checkout and found only
# by manual audit. Each axis is reported on its own line, so one run names
# everything wrong; exit 1 when any checked axis failed, 0 when all pass. A
# missing sibling checkout is "skip", never a failure.
#
# Usage: scripts/verify-release.sh   (from anywhere)
# Siblings expected next to this repository: ../eka-core, ../eka-mcp,
# ../eka-standard.
set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SIBLINGS=$(CDPATH= cd -- "$ROOT/.." && pwd)

CHECKED=0
FAILED=0
SKIPPED=0

pass() { CHECKED=$((CHECKED + 1)); printf 'ok    %s\n' "$1"; }
fail() { CHECKED=$((CHECKED + 1)); FAILED=$((FAILED + 1)); printf 'FAIL  %s\n' "$1"; }
skip() { SKIPPED=$((SKIPPED + 1)); printf 'skip  %s\n' "$1"; }

# The eka-core version one go.mod requires: the line that decides which core
# the released binary is built against.
core_pin() {
  sed -n 's|^[[:space:]]*github.com/maleolabs/eka-core[[:space:]]\{1,\}\(v[0-9][0-9.]*\)$|\1|p' "$1" | head -1
}

# The standard states its corpus version in prose and tables, never as a
# machine field; take the first X.Y of the line that states it.
first_xy() {
  grep -o '[0-9][0-9]*\.[0-9][0-9]*' | head -1
}

# --- axis 1: pins resolve to a real eka-core tag ---------------------------
if [ -d "$SIBLINGS/eka-core" ]; then
  for MODULE in eka-cli eka-mcp; do
    if [ ! -f "$SIBLINGS/$MODULE/go.mod" ]; then
      skip "$MODULE: ../$MODULE absent, eka-core pin unresolved"
      continue
    fi
    PIN=$(core_pin "$SIBLINGS/$MODULE/go.mod")
    if [ -z "$PIN" ]; then
      fail "$MODULE: go.mod requires no github.com/maleolabs/eka-core version"
    elif [ -n "$(git -C "$SIBLINGS/eka-core" tag --list "$PIN" 2>/dev/null)" ]; then
      pass "$MODULE: eka-core $PIN is a tag in ../eka-core"
    else
      fail "$MODULE: eka-core $PIN is not a tag in ../eka-core"
    fi
  done
else
  for MODULE in eka-cli eka-mcp; do
    skip "$MODULE: ../eka-core absent, eka-core pin unresolved"
  done
fi

# --- axis 2: the standard agrees with itself ------------------------------
if [ -d "$SIBLINGS/eka-standard" ]; then
  STANDARD="$SIBLINGS/eka-standard"
  EKA_V=$(sed -n 's/^Version[[:space:]]\{1,\}//p' "$STANDARD/EKA" | head -1)
  VERSIONING_V=$(grep -m1 'EKA Standard [0-9]' "$STANDARD/specification/versioning.md" | first_xy)
  EXCHANGE_V=$(grep -m1 'Standard corpus version' "$STANDARD/specification/exchange.md" | first_xy)
  if [ -n "$EKA_V" ] && [ "$EKA_V" = "$VERSIONING_V" ] && [ "$EKA_V" = "$EXCHANGE_V" ]; then
    pass "eka-standard: corpus $EKA_V agrees in EKA, versioning.md and exchange.md"
  else
    fail "eka-standard: corpus '${EKA_V:-none}' in EKA, '${VERSIONING_V:-none}' in versioning.md, '${EXCHANGE_V:-none}' in exchange.md"
  fi
else
  skip "eka-standard: ../eka-standard absent"
fi

# --- axis 3: vendored digests match the files -----------------------------
# Pin paths carry no spaces, so a word-split loop is the POSIX stand-in for
# the array this script deliberately avoids.
PINS=$(sed -n 's|^  \([^ ][^:]*\): \([0-9a-f]\{64\}\)$|\1:\2|p' "$ROOT/vendored.yaml")
[ -z "$PINS" ] && fail "vendored: vendored.yaml lists no digests"
for PAIR in $PINS; do
  FILE=${PAIR%:*}
  WANT=${PAIR##*:}
  if [ ! -f "$ROOT/$FILE" ]; then
    fail "vendored: $FILE is pinned but missing"
    continue
  fi
  GOT=$(sha256sum "$ROOT/$FILE" | cut -d' ' -f1)
  if [ "$GOT" = "$WANT" ]; then
    pass "vendored: $FILE matches its pinned digest"
  else
    fail "vendored: $FILE drifted (pinned $WANT, file $GOT)"
  fi
done

# --- axis 4: the corpus copy in eka-core matches the published one --------
# The corpus is normative only while a shipped copy provably equals the
# published bytes: eka-core carries the fixtures its harness runs, eka-standard
# publishes them with SHA256SUMS. Without this check the two diverge silently
# and the corpus stops describing the format while both repositories stay
# green — the same write-only copy problem the corpus exists to expose.
SUMS="$SIBLINGS/eka-standard/conformance/exchange/SHA256SUMS"
CORPUS="$SIBLINGS/eka-core/exchange/testdata/corpus"
if [ ! -f "$SUMS" ]; then
  skip "corpus: ../eka-standard publishes no conformance/exchange/SHA256SUMS"
elif [ ! -d "$CORPUS" ]; then
  skip "corpus: ../eka-core has no exchange/testdata/corpus"
else
  OUT=$(cd "$CORPUS" && sha256sum -c "$SUMS" 2>&1)
  if [ $? -eq 0 ]; then
    pass "corpus: ../eka-core's copy matches the published SHA256SUMS"
  else
    fail "corpus: $(printf '%s\n' "$OUT" | grep -v ': OK$' | head -3 | tr '\n' ' ')"
  fi
fi

# --- axis 5: the generated README artifacts still describe this repository ---
# The demo recording and the latency table are committed artifacts that a script
# in this repository derives, so this axis needs no sibling checkout and works in
# release CI as well as locally. It re-runs nothing: --check compares the demo
# against its recorded digest and the table and chart against the record they came
# from. Re-recording and re-measuring stay deliberate acts, which is what keeps
# this axis cheap enough to stand in front of every tag.
for GENERATED in media bench; do
  GEN_SCRIPT="$ROOT/scripts/$GENERATED.sh"
  # Each script owns what its own --check proves, and the two prove different
  # things: bench re-derives the table, the chart and the README block from the
  # record, while media only compares the GIF against its recorded digest — which
  # detects a replaced file, not a stale screenshot, because a recording is not
  # byte-reproducible. Naming that here keeps the gate from overclaiming.
  case "$GENERATED" in
    bench) GEN_CLAIM="record, table, chart and README block agree" ;;
    media) GEN_CLAIM="GIF matches its recorded digest (detects replacement, not staleness)" ;;
  esac
  if [ ! -f "$GEN_SCRIPT" ]; then
    skip "$GENERATED: scripts/$GENERATED.sh is absent"
    continue
  fi
  OUT=$(cd "$ROOT" && sh "$GEN_SCRIPT" --check 2>&1)
  if [ $? -eq 0 ]; then
    pass "$GENERATED: $GEN_CLAIM"
  else
    REASON=$(printf '%s\n' "$OUT" | grep '^FAIL' | head -2 | tr '\n' ' ')
    fail "$GENERATED: ${REASON:-scripts/$GENERATED.sh --check failed}"
  fi
done

printf '\nverdict: %d axes checked, %d failed, %d skipped\n' "$CHECKED" "$FAILED" "$SKIPPED"
[ "$FAILED" -eq 0 ]