#!/bin/sh
# media.sh — record the README's demo GIF from docs/media/readme.tape.
#
# Why: the README's demo is a binary artifact, so it cannot be reviewed like
# text, and it silently rots the moment a projection changes. Recording it from a
# committed tape makes the GIF regenerable and the tape reviewable; the recorded
# digest makes a replaced or hand-edited GIF a --check failure rather than a
# thing nobody notices.
#
# What the digest does not do, and this is measured rather than assumed: prove
# the GIF is current. VHS encodes from a live session, so two recordings of
# identical screens already differ — re-recording this tape twice moves ~450 of
# 678k pixels, spread across the whole frame, with no change in content. The
# digest therefore detects a swapped file, never a stale screenshot. Judging
# staleness stays a human act, which is why the tape pins the terminal geometry
# the projections were rendered at: a projection that outgrows it is the failure
# a reader would actually notice.
#
# The tape is deterministic in the sense that matters for review — the fixture,
# the binary and $HOME are prepared fresh in a fixed directory, so the same tape
# always shows the same screens — but never byte for byte.
#
# Usage:
#   sh scripts/media.sh --write   render docs/media/demo.gif and its digest
#   sh scripts/media.sh --check   verify the committed GIF matches its digest
#
# Requires vhs and ttyd for --write (https://github.com/charmbracelet/vhs).
# Exit codes: 0 ok; 1 drift, or missing/failed tooling; 2 usage.
set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MEDIA="$ROOT/docs/media"
TAPE="$MEDIA/readme.tape"
GIF="$MEDIA/demo.gif"
DIGEST="$MEDIA/demo.gif.sha256"
DEMO="${TMPDIR:-/tmp}/eka-demo"
README="$ROOT/README.md"

case "${1:-}" in
  --write)
    for tool in vhs ttyd; do
      command -v "$tool" >/dev/null 2>&1 || {
        printf 'FAIL  media: %s not on PATH; install vhs and ttyd first (see the header of this script)\n' "$tool" >&2
        exit 1
      }
    done
    [ -f "$TAPE" ] || { printf 'FAIL  media: %s is missing\n' "${TAPE#$ROOT/}" >&2; exit 1; }

    # Fresh fixture every time: the recorded scrollback must not depend on what
    # an earlier run left in the workspace.
    rm -rf "$DEMO"
    mkdir -p "$DEMO/bin" "$DEMO/home" "$DEMO/repo"
    (cd "$ROOT" && go build -buildvcs=false -o "$DEMO/bin/eka" ./cmd/eka) || {
      printf 'FAIL  media: go build ./cmd/eka failed\n' >&2
      exit 1
    }
    cp -r "$ROOT/testdata/view/valid/." "$DEMO/repo/"
    # Sync once before recording so the tour starts from a populated workspace.
    (cd "$DEMO/repo" && HOME="$DEMO/home" EKA_HOME="$DEMO/home/.eka" "$DEMO/bin/eka" sync) >/dev/null 2>&1 || {
      printf 'FAIL  media: seeding the fixture workspace failed\n' >&2
      exit 1
    }

    # vhs resolves Output relative to its own working directory, and the tape
    # names docs/media/demo.gif, so record from the repository root — and hand
    # the tape the fixture directory, because the recording must happen there,
    # not in this checkout.
    (cd "$ROOT" && HOME="$DEMO/home" EKA_HOME="$DEMO/home/.eka" EKA_DEMO_REPO="$DEMO/repo" PATH="$DEMO/bin:$PATH" vhs "$TAPE") || {
      printf 'FAIL  media: vhs failed\n' >&2
      exit 1
    }
    [ -s "$GIF" ] || { printf 'FAIL  media: vhs produced no %s\n' "${GIF#$ROOT/}" >&2; exit 1; }
    (cd "$MEDIA" && sha256sum demo.gif > demo.gif.sha256)
    printf 'ok    media: recorded %s (%s) and its digest\n' "${GIF#$ROOT/}" "$(du -h "$GIF" | cut -f1)"
    ;;
  --check)
    rc=0
    if [ ! -s "$GIF" ]; then
      printf 'FAIL  media: %s is missing, run sh scripts/media.sh --write\n' "${GIF#$ROOT/}"; rc=1
    elif (cd "$MEDIA" && sha256sum -c demo.gif.sha256 >/dev/null 2>&1); then
      printf 'ok    media: %s matches its recorded digest (%s)\n' "${GIF#$ROOT/}" "$(du -h "$GIF" | cut -f1)"
    else
      printf 'FAIL  media: %s does not match %s, re-record and commit both\n' "${GIF#$ROOT/}" "${DIGEST#$ROOT/}"; rc=1
    fi
    if grep -qF 'src="docs/media/demo.gif"' "$README"; then
      printf 'ok    media: README references the demo\n'
    else
      printf 'FAIL  media: README no longer references docs/media/demo.gif\n'; rc=1
    fi
    exit $rc
    ;;
  *)
    printf 'Usage: %s --write|--check\n' "$0" >&2
    exit 2
    ;;
esac
