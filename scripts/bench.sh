#!/bin/sh
# bench.sh — measure the CLI's latency profile and record it as the artifacts
# the README quotes: a raw JSON record, a markdown table, and an SVG chart. The
# table is spliced into README.md between the eka:bench markers.
#
# Why: every number this project publishes is a claim, and the house rule is
# that a claim is generated and checked, never typed (standardembed_readme_test.go
# checks the version line, vendored.yaml pins digests). Latency has no digest —
# it differs per machine and per run — so the record keeps the measurement
# together with the machine it was taken on, and --check gates only what is
# deterministic: the markdown and the chart must be exactly what the record
# derives to, and the README must quote the markdown verbatim. A stale record is
# therefore a --check failure, and re-measuring is a deliberate --write.
#
# Usage:
#   sh scripts/bench.sh --write   build, measure, rewrite json/md/svg and README
#   sh scripts/bench.sh --check   derive from the record and diff; never measures
#
# Env: EKA_BENCH_RUNS (default 5) — repetitions per scenario; the best run is
# reported, because a shared machine only ever adds latency, never removes it.
#
# Exit codes: 0 ok; 1 drift detected (--check) or measurement failed; 2 usage.
set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BENCH="$ROOT/docs/bench"
RECORD="$BENCH/benchmarks.json"
TABLE="$BENCH/benchmarks.md"
CHART="$BENCH/benchmarks.svg"
README="$ROOT/README.md"
BLOCK_START='<!-- eka:bench:start -->'
BLOCK_END='<!-- eka:bench:end -->'
RUNS=${EKA_BENCH_RUNS:-5}

# Scenarios, one line each: id|workdir|command. workdir "fix" is a private copy
# of testdata/view/valid (a conformant 40-file repository); "repo" is a clean
# export of this checkout's HEAD, which is what the code index costs something to
# walk.
#
# An id ending in "-cold" wipes the derived cache before every sample, so every
# sample pays the index build rather than reading it back. The source tree is
# left to the OS page cache, so the figure is index cost, not disk I/O — a
# genuinely cold disk only ever adds to it.
SCENARIOS='startup-version|fix|eka version
validate|fix|eka validate .
sync|fix|eka sync
view-execution|fix|eka view execution
status|fix|eka status
code-context-cold|repo|eka code-context "context engine" --limit 8
code-context-warm|repo|eka code-context "context engine" --limit 8
code-discover|repo|eka code-discover "context engine" --limit 8'

TMP=

# --- measurement ------------------------------------------------------------

build() {
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT INT TERM
  if ! (cd "$ROOT" && go build -buildvcs=false -o "$TMP/eka" ./cmd/eka); then
    printf 'FAIL  bench: go build ./cmd/eka failed\n' >&2
    exit 1
  fi
  EKA="$TMP/eka"
  # Isolated HOME before anything runs: the store, the derived cache and the
  # fixture copy all live under TMP, so a measurement never touches the
  # developer's own workspace and never depends on it either.
  HOME="$TMP/home"
  EKA_HOME="$TMP/home/.eka"
  export HOME EKA_HOME
  mkdir -p "$HOME" "$TMP/fixture" "$TMP/repo"
  cp -r "$ROOT/testdata/view/valid/." "$TMP/fixture/"
  # The "repo" scenarios run against a clean export of HEAD, not against the
  # working tree. The code index walks the filesystem, so an untracked file left
  # in the checkout moves the number: a stray 23 MB eka.exe in this repository's
  # own working directory costs the index ~135 ms, which is more than the index
  # itself. Exporting makes the figure describe the committed sources and nothing
  # else — the same thing a fresh clone would measure.
  if ! (cd "$ROOT" && git archive HEAD) | tar -x -C "$TMP/repo"; then
    printf 'FAIL  bench: exporting HEAD for the repo scenarios failed\n' >&2
    exit 1
  fi
  : > "$TMP/results.tsv"
}

# one(dir, command, runs, cold) -> best wall-clock milliseconds on stdout
one() {
  dir=$1; cmd=$2; runs=$3; cold=$4
  args=${cmd#eka }
  best=999999999; i=0
  while [ "$i" -lt "$runs" ]; do
    [ "$cold" = 1 ] && rm -rf "$HOME/.cache/eka/codegraph"
    t0=$(date +%s%N)
    (cd "$dir" && sh -c "$EKA $args") >/dev/null 2>&1
    rc=$?
    t1=$(date +%s%N)
    if [ "$rc" -ne 0 ]; then
      printf 'FAIL  bench: %s exited %s\n' "$cmd" "$rc" >&2
      return 1
    fi
    ms=$(( (t1 - t0) / 1000000 ))
    [ "$ms" -lt "$best" ] && best=$ms
    i=$((i + 1))
  done
  printf '%s' "$best"
}

measure() {
  # Read from a file rather than a pipe: a pipe would run the loop in a subshell,
  # and a failing scenario has to abort the run, not just its own subshell.
  printf '%s\n' "$SCENARIOS" > "$TMP/scenarios.txt"
  while IFS='|' read -r id where cmd; do
    [ -z "$id" ] && continue
    case "$where" in
      fix) dir=$TMP/fixture ;;
      *)   dir=$TMP/repo ;;
    esac
    cold=0; runs=$RUNS
    case "$id" in *-cold) cold=1 ;; esac
    ms=$(one "$dir" "$cmd" "$runs" "$cold") || exit 1
    printf '%s\t%s\t%s\n' "$id" "$cmd" "$ms" >> "$TMP/results.tsv"
  done < "$TMP/scenarios.txt"
  [ -s "$TMP/results.tsv" ] || { printf 'FAIL  bench: no scenario ran\n' >&2; exit 1; }
}

write_record() {
  cpu=$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//')
  cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 0)
  cache=$(du -sk "$HOME/.cache/eka/codegraph" 2>/dev/null | cut -f1)
  [ -z "$cache" ] && cache=0
  {
    printf '{\n'
    printf '  "schema": "eka/benchmarks/1",\n'
    printf '  "statistic": "min",\n'
    printf '  "runs": %s,\n' "$RUNS"
    printf '  "os": "%s",\n' "$(uname -s)"
    printf '  "kernel": "%s",\n' "$(uname -r)"
    printf '  "cpu": "%s",\n' "$cpu"
    printf '  "cores": %s,\n' "$cores"
    printf '  "goVersion": "%s",\n' "$(go version | awk '{print $3}')"
    printf '  "binaryBytes": %s,\n' "$(wc -c < "$EKA" | tr -d ' ')"
    printf '  "codegraphCacheKiB": %s,\n' "$cache"
    printf '  "scenarios": [\n'
  } > "$TMP/benchmarks.json"
  # JSON-escape the command, then strip the trailing comma off the last row so
  # the array closes cleanly without a sentinel element.
  awk -F'\t' '{
    cmd = $2
    gsub(/\\/, "\\\\", cmd)
    gsub(/"/, "\\\"", cmd)
    printf "    {\"id\": \"%s\", \"command\": \"%s\", \"ms\": %s},\n", $1, cmd, $3
  }' "$TMP/results.tsv" >> "$TMP/benchmarks.json"
  sed '$ s/,$//' "$TMP/benchmarks.json" > "$TMP/record.tmp" || true
  mv "$TMP/record.tmp" "$TMP/benchmarks.json"
  {
    printf '  ]\n'
    printf '}\n'
  } >> "$TMP/benchmarks.json"
}

# The readers below are deliberately coupled to the writer above: same key
# order, same two-space indent, one scenario per line. Both live in this one
# file so they cannot drift apart.
jstr() { sed -n "s/^  \"$1\": \"\(.*\)\",$/\1/p" "$RECORD" | head -1; }
jnum() { sed -n "s/^  \"$1\": \([0-9][0-9]*\),$/\1/p" "$RECORD" | head -1; }

scenarios() {
  sed -n 's/^    {"id": "\([^"]*\)", "command": "\(.*\)", "ms": \([0-9][0-9]*\)},\?$/\1|\2|\3/p' \
    "$RECORD" | sed 's/\\"/"/g'
}

# --- render -----------------------------------------------------------------

render_md() {
  [ -f "$RECORD" ] || return 1
  printf '<!-- generated by scripts/bench.sh --write; do not edit by hand -->\n\n'
  printf 'Best of %s runs, wall-clock milliseconds, measured on %s (%s threads),\n' \
    "$(jnum runs)" "$(jstr cpu)" "$(jnum cores)"
  printf '%s %s, %s. The record with the full machine context is\n' \
    "$(jstr os)" "$(jstr kernel)" "$(jstr goVersion)"
  printf '[`docs/bench/benchmarks.json`](docs/bench/benchmarks.json). The chart\n'
  printf 'beside it is [`docs/bench/benchmarks.svg`](docs/bench/benchmarks.svg).\n\n'
  printf '| Scenario | Command | ms |\n'
  printf '| --- | --- | ---: |\n'
  scenarios | while IFS='|' read -r id cmd ms; do
    printf '| `%s` | `%s` | %s |\n' "$id" "$cmd" "$ms"
  done
  printf '\nThe two `code-context` rows are the same query. The cold row wipes the\n'
  printf 'derived cache before every sample, so it pays for walking and parsing this\n'
  printf "repository's Go sources; the warm row answers from the cache that run wrote.\n"
  printf 'The index is paid once per source revision, not once per question. The\n'
  printf 'source tree stays in the OS page cache, so the cold figure is index cost\n'
  printf 'rather than disk I/O. These are one machine, named in the record — not a\n'
  printf 'claim about yours.\n'
}

render_svg() {
  [ -f "$RECORD" ] || return 1
  rows=$(scenarios)
  n=$(printf '%s\n' "$rows" | grep -c .)
  [ "$n" -gt 0 ] || return 1
  max=$(printf '%s\n' "$rows" | cut -d'|' -f3 | sort -n | tail -1)
  [ "$max" -gt 0 ] || max=1

  w=760; label=210; track=360; rowh=40; top=56
  h=$(( top + n * rowh + 30 ))
  printf '<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s" role="img" aria-label="eka-cli latency per scenario">\n' "$w" "$h" "$w" "$h"
  printf '  <title>eka-cli latency: best of %s runs, milliseconds</title>\n' "$(jnum runs)"
  printf '  <desc>Measured on %s.</desc>\n' "$(jstr cpu)"
  printf '  <g font-family="ui-monospace,SFMono-Regular,Menlo,monospace" font-size="13">\n'
  y=$top
  printf '%s\n' "$rows" | while IFS='|' read -r id cmd ms; do
    bw=$(( (ms * track + max - 1) / max ))
    [ "$bw" -lt 2 ] && bw=2
    # Amber for the cold index run (the palette's warning hue): it is the
    # outlier the chart exists to show, not an error.
    case "$id" in *-cold) fill='#ffaf5f' ;; *) fill='#5fafff' ;; esac
    printf '    <text x="0" y="%s" fill="#8a8a8a">%s</text>\n' "$((y + 14))" "$id"
    printf '    <rect x="%s" y="%s" width="%s" height="20" rx="3" fill="#8a8a8a" opacity="0.14"/>\n' "$label" "$y" "$track"
    printf '    <rect x="%s" y="%s" width="%s" height="20" rx="3" fill="%s"/>\n' "$label" "$y" "$bw" "$fill"
    printf '    <text x="%s" y="%s" fill="#8a8a8a">%s</text>\n' "$((label + track + 12))" "$((y + 14))" "$ms"
    y=$(( y + rowh ))
  done
  printf '    <text x="0" y="%s" fill="#8a8a8a" opacity="0.8">cold code index (amber) vs everything else</text>\n' "$((h - 10))"
  printf '  </g>\n</svg>\n'
}

# The markdown is the contract: README quotes it verbatim between the markers.
block_of() { awk -v s="$BLOCK_START" -v e="$BLOCK_END" 'index($0,s){f=1;next} index($0,e){f=0} f' "$README"; }

splice() {
  awk -v s="$BLOCK_START" -v e="$BLOCK_END" -v b="$TABLE" '
    index($0, s) { print; while ((getline l < b) > 0) print l; close(b); print ""; skip=1; next }
    index($0, e) { skip=0 }
    !skip { print }
  ' "$README"
}

markers_ok() {
  grep -qF "$BLOCK_START" "$README" && grep -qF "$BLOCK_END" "$README"
}

# --- modes ------------------------------------------------------------------

case "${1:-}" in
  --write)
    markers_ok || {
      printf 'FAIL  bench: README.md has no %s / %s markers\n' "$BLOCK_START" "$BLOCK_END" >&2
      exit 1
    }
    build
    measure
    write_record
    mkdir -p "$BENCH"
    cp "$TMP/benchmarks.json" "$RECORD"
    render_md > "$TABLE"
    render_svg > "$CHART"
    splice > "$TMP/README.md" && mv "$TMP/README.md" "$README"
    printf 'ok    bench: recorded %s scenarios -> %s, %s, %s\n' \
      "$(scenarios | grep -c .)" "${RECORD#$ROOT/}" "${TABLE#$ROOT/}" "${CHART#$ROOT/}"
    ;;
  --check)
    [ -f "$RECORD" ] || { printf 'FAIL  bench: %s is missing, run sh scripts/bench.sh --write\n' "${RECORD#$ROOT/}"; exit 1; }
    rc=0
    while IFS=: read -r file fn; do
      if [ ! -f "$file" ]; then
        printf 'FAIL  bench: %s is missing, run sh scripts/bench.sh --write\n' "${file#$ROOT/}"; rc=1; continue
      fi
      if "$fn" | diff -q - "$file" >/dev/null 2>&1; then
        printf 'ok    bench: %s matches the record\n' "${file#$ROOT/}"
      else
        printf 'FAIL  bench: %s drifted from the record, run sh scripts/bench.sh --write\n' "${file#$ROOT/}"; rc=1
      fi
    done <<EOF
$TABLE:render_md
$CHART:render_svg
EOF
    # Command substitution strips the blank line splice leaves before the closing
    # marker, so the comparison is body-against-body, not body-plus-padding.
    if ! markers_ok; then
      printf 'FAIL  bench: README.md has no %s / %s markers\n' "$BLOCK_START" "$BLOCK_END"; rc=1
    elif printf '%s\n' "$(block_of)" | diff -q - "$TABLE" >/dev/null 2>&1; then
      printf 'ok    bench: README quotes the record verbatim\n'
    else
      printf 'FAIL  bench: README block drifted from %s, run sh scripts/bench.sh --write\n' "${TABLE#$ROOT/}"; rc=1
    fi
    exit $rc
    ;;
  *)
    printf 'Usage: %s --write|--check\n' "$0" >&2
    exit 2
    ;;
esac
