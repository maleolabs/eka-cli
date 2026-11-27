#!/bin/sh
# install.sh — Install the EKA CLI
#
# Usage:
#   curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh -s -- --version v0.1.0
#   curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh -s -- --to ~/.local/bin
#   curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh -s -- --completion zsh
#   curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh -s -- --no-completion
#
# Downloads the latest (or specified) EKA release binary for the current
# OS and architecture from the GitHub Release asset registry, verifies
# its checksum against the release's SHA256SUMS.txt, and installs it to
# /usr/local/bin (or a custom path via --to).
#
# After the binary is installed, shell completion is registered:
#   - on an interactive terminal (stdin or /dev/tty available) the
#     installer prompts for the shell (bash/zsh/fish/none) with the
#     detected default shell preselected;
#   - non-interactive runs (CI, pipes without a terminal) auto-register
#     the detected default shell, or skip when none is detectable;
#   - --completion <shell> forces a shell, --no-completion skips
#     completion setup entirely. Completion is best-effort: a failure
#     never fails the install (the binary is already in place).
#
# Installer trust model: verification is FAIL-CLOSED. The release
# workflow always publishes SHA256SUMS.txt alongside the binaries; a
# checksum file that cannot be fetched — or a checksum mismatch —
# aborts the install. No binary is ever installed unverified.
#
# Supported platforms:
#   - Linux (amd64, arm64)
#   - macOS (amd64, arm64)

set -eu

# ── Helpers ────────────────────────────────────────────────────────
_now() {
  date +%s 2>/dev/null || echo "0"
}

_elapsed() {
  _start="$1"
  _end="$2"
  _diff=$((_end - _start))
  if [ "$_diff" -lt 60 ]; then
    echo "${_diff}s"
  else
    _mins=$((_diff / 60))
    _secs=$((_diff % 60))
    echo "${_mins}m ${_secs}s"
  fi
}

# _run_step executes a command and reports success/failure with timing.
# Usage: _run_step "Step name" command args...
_run_step() {
  _name="$1"
  shift
  _begin=$(_now)

  _output=$("$@" 2>&1) && _rc=0 || _rc=$?

  _end=$(_now)
  _time=$(_elapsed "$_begin" "$_end")

  if [ "$_rc" -eq 0 ]; then
    printf "  ✓ %-40s (%s)\n" "$_name" "$_time"
  else
    printf "  ✗ %-40s (%s)\n" "$_name" "$_time"
    if [ -n "$_output" ]; then
      printf "    %s\n" "$_output"
    fi
    exit 1
  fi
}

# ── Config ─────────────────────────────────────────────────────────
REPO="maleolabs/eka-cli"
DEFAULT_INSTALL_DIR="/usr/local/bin"
VERSION="latest"
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
COMPLETION_SHELL=""
COMPLETION_ENABLED=1

# ── Parse args ─────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    --to)
      INSTALL_DIR="$2"
      shift 2
      ;;
    --completion)
      COMPLETION_SHELL="$2"
      shift 2
      ;;
    --no-completion)
      COMPLETION_ENABLED=0
      shift
      ;;
    --help)
      echo "Usage: install.sh [--version vX.Y.Z] [--to <dir>] [--completion <shell>|--no-completion]"
      echo ""
      echo "  --version vX.Y.Z            Install a specific version (default: latest)"
      echo "  --to <dir>                  Install to a custom directory (default: /usr/local/bin)"
      echo "  --completion <shell>        Register shell completion: bash, zsh, fish or none"
      echo "                              (default: interactive prompt, or auto-detect the"
      echo "                              default shell when non-interactive)"
      echo "  --no-completion             Skip shell completion setup"
      exit 0
      ;;
    *)
      echo "Unknown argument: $1"
      echo "Usage: install.sh [--version vX.Y.Z] [--to <dir>] [--completion <shell>|--no-completion]"
      exit 1
      ;;
  esac
done

if [ -n "$COMPLETION_SHELL" ]; then
  case "$COMPLETION_SHELL" in
    bash|zsh|fish|none) ;;
    *)
      echo "Invalid --completion value: $COMPLETION_SHELL (expected bash, zsh, fish or none)"
      exit 1
      ;;
  esac
fi

# ── Header ─────────────────────────────────────────────────────────
TOTAL_START=$(_now)
echo ""
echo "EKA CLI Installer"
echo "─────────────────"
echo ""

# ── Detect platform ────────────────────────────────────────────────
_detect_platform() {
  _os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  _arch="$(uname -m)"

  case "$_os" in
    linux)   ;;
    darwin)  _os="darwin" ;;
    *)
      # stderr: _detect_platform output is captured by the caller's
      # command substitution; diagnostics must not be swallowed.
      echo "Unsupported OS '$_os'. Only Linux and macOS are supported." >&2
      exit 1
      ;;
  esac

  case "$_arch" in
    x86_64|amd64) _arch="amd64" ;;
    aarch64|arm64) _arch="arm64" ;;
    *)
      echo "Unsupported architecture '$_arch'. Only amd64 and arm64 are supported." >&2
      exit 1
      ;;
  esac

  echo "${_os}/${_arch}"
}

_detect_result=$(_detect_platform)
printf "  ✓ %-40s\n" "Platform: $_detect_result"

OS="$(echo "$_detect_result" | cut -d'/' -f1)"
ARCH="$(echo "$_detect_result" | cut -d'/' -f2)"
BINARY="eka-${OS}-${ARCH}"

# ── Resolve download URL ───────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
  BASE_URL="https://github.com/$REPO/releases/latest/download"
else
  BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
fi

DOWNLOAD_URL="$BASE_URL/$BINARY"
CHECKSUM_URL="$BASE_URL/SHA256SUMS.txt"

# ── Create temp directory ──────────────────────────────────────────
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# ── Download binary ────────────────────────────────────────────────
# Restricted to https (--proto =https): release material is never
# fetched over a plaintext channel.
_download() {
  _http_code="$(curl -fsSL --proto =https -w '%{http_code}' -o "$TMPDIR/$BINARY" "$DOWNLOAD_URL" 2>/dev/null || true)"
  if [ "$_http_code" != "200" ]; then
    echo "Download failed (HTTP $_http_code)"
    echo "URL: $DOWNLOAD_URL"
    exit 1
  fi
  chmod +x "$TMPDIR/$BINARY"
}

_run_step "Download $BINARY" _download

# ── Verify checksum ────────────────────────────────────────────────
# Fail-closed: a checksum file that cannot be fetched — or a checksum
# mismatch — aborts the install. The release workflow always publishes
# SHA256SUMS.txt, so an unverifiable download is a broken release.
_verify_checksum() {
  if ! curl -fsSL --proto =https -o "$TMPDIR/SHA256SUMS.txt" "$CHECKSUM_URL" 2>/dev/null; then
    echo "Checksum file not available - refusing to install $BINARY unverified (fail-closed)."
    echo "URL: $CHECKSUM_URL"
    exit 1
  fi

  # Extract expected hash from SHA256SUMS.txt. The file may contain
  # "binaries/eka-linux-amd64" or just "eka-linux-amd64".
  EXPECTED_HASH=""
  while IFS= read -r line; do
    case "$line" in
      *"$BINARY")
        EXPECTED_HASH="${line%%  *}"
        break
        ;;
    esac
  done < "$TMPDIR/SHA256SUMS.txt"

  if [ -z "$EXPECTED_HASH" ]; then
    echo "No checksum entry for $BINARY in SHA256SUMS.txt - refusing to install (fail-closed)."
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_HASH="$(sha256sum "$TMPDIR/$BINARY" | cut -d' ' -f1)"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_HASH="$(shasum -a 256 "$TMPDIR/$BINARY" | cut -d' ' -f1)"
  else
    echo "No sha256 tool found (sha256sum or shasum) - refusing to install $BINARY unverified (fail-closed)."
    exit 1
  fi

  if [ "$EXPECTED_HASH" != "$ACTUAL_HASH" ]; then
    echo "Checksum mismatch - downloaded binary may be corrupted or tampered."
    echo "Expected: $EXPECTED_HASH"
    echo "Actual:   $ACTUAL_HASH"
    exit 1
  fi
}

_run_step "Verify checksum" _verify_checksum

# ── Install ────────────────────────────────────────────────────────
_install() {
  mkdir -p "$INSTALL_DIR"

  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMPDIR/$BINARY" "$INSTALL_DIR/eka"
  else
    sudo mv "$TMPDIR/$BINARY" "$INSTALL_DIR/eka"
  fi
}

_run_step "Install to $INSTALL_DIR/eka" _install

# ── Shell completion ──────────────────────────────────────────────
# Completion setup is best-effort and NEVER fails the install: the
# binary is already in place at this point. The shell target resolves
# as follows (documented contract):
#
#   --completion <shell>       explicit, non-interactive (bash|zsh|fish|none)
#   --no-completion            skip
#   interactive terminal       prompt with the detected default shell
#                              preselected (Enter accepts the default)
#   non-interactive            auto-register the detected default shell;
#                              skip when no supported shell is detectable
#
# Prompt output goes to stderr and input is read from /dev/tty when
# available: `curl ... | sh` keeps working interactively even though
# stdin is a pipe. All registration is idempotent (a marker line is
# grepped before appending) and per-user (no sudo ever touches the
# user's rc files).

# _detect_default_shell prints the user's default shell (bash/zsh/fish)
# or exits 1 when nothing supported is found.
_detect_default_shell() {
  _s=""
  if [ -n "${SHELL:-}" ]; then
    _s="$(basename "$SHELL" 2>/dev/null || true)"
  fi
  case "$_s" in
    bash|zsh|fish) echo "$_s"; return 0 ;;
  esac
  # Fallback via the passwd entry (getent is Linux-only; macOS has no
  # getent, and the SHELL env var is set for interactive users there).
  _u="$(id -un 2>/dev/null || true)"
  if [ -n "$_u" ] && command -v getent >/dev/null 2>&1; then
    _pw="$(getent passwd "$_u" 2>/dev/null | cut -d: -f7 || true)"
    if [ -n "$_pw" ]; then
      _s="$(basename "$_pw" 2>/dev/null || true)"
      case "$_s" in
        bash|zsh|fish) echo "$_s"; return 0 ;;
      esac
    fi
  fi
  return 1
}

# _tty_available reports whether a controlling terminal exists and can
# be opened. /dev/tty is world-readable, so a `-r` check alone is not
# enough: open() fails with ENXIO when there is no controlling
# terminal (CI runners, cron, daemons). An actual open is the only
# reliable probe. The probe runs in a subshell so a failed open (and
# its shell diagnostic) can never disturb this shell — the 2>/dev/null
# on the subshell catches the diagnostic, which a redirection on the
# `exec` itself would not (input redirections are processed before
# stderr is redirected).
_tty_available() {
  if [ -t 0 ]; then
    return 0
  fi
  ( exec 3< /dev/tty ) 2>/dev/null
}

# _prompt_read reads one line from the controlling terminal (or stdin
# when stdin itself is a TTY), so prompts work under `curl | sh`.
# Callers guarantee a terminal exists via _tty_available.
_prompt_read() {
  if [ -t 0 ]; then
    IFS= read -r _ans || return 1
  else
    IFS= read -r _ans < /dev/tty 2>/dev/null || return 1
  fi
}

# _prompt_shell_selection prints the menu (stderr — the caller captures
# stdout) and echoes the chosen shell.
_prompt_shell_selection() {
  _detected="$1"
  printf '  Shell completion (Tab completion for eka):\n' >&2
  printf '    1) bash\n    2) zsh\n    3) fish\n    4) none\n' >&2
  while :; do
    printf '  Choose shell [1-4] (Enter = %s): ' "$_detected" >&2
    if _prompt_read; then
      if [ -z "$_ans" ]; then
        echo "$_detected"
        return
      fi
      case "$_ans" in
        1) echo "bash"; return ;;
        2) echo "zsh"; return ;;
        3) echo "fish"; return ;;
        4) echo "none"; return ;;
        *) printf '    invalid choice: %s\n' "$_ans" >&2 ;;
      esac
    else
      echo "none"
      return
    fi
  done
}

# _resolve_completion_shell echoes the final shell target ("none" to skip).
_resolve_completion_shell() {
  if [ -n "$COMPLETION_SHELL" ]; then
    echo "$COMPLETION_SHELL"
    return
  fi
  [ "$COMPLETION_ENABLED" -eq 0 ] && { echo "none"; return; }
  _detected="$(_detect_default_shell || echo none)"
  if _tty_available; then
    _prompt_shell_selection "$_detected"
  else
    echo "$_detected"
  fi
}

_register_bash() {
  _rc="${HOME}/.bashrc"
  _marker='# eka shell completion'
  if [ -f "$_rc" ] && grep -qF "$_marker" "$_rc" 2>/dev/null; then
    echo "    bash: already registered in $_rc (skip)"
    return 0
  fi
  {
    printf '\n%s\n' "$_marker"
    # The command guard defers to PATH: once the user adds the install
    # dir to PATH, completion activates without error spam.
    printf '%s\n' 'if command -v eka >/dev/null 2>&1; then source <(eka completion bash); fi'
  } >> "$_rc"
  echo "    bash: registered in $_rc (reload: source ~/.bashrc)"
}

_register_zsh() {
  _zdir="${HOME}/.zfunc"
  _rc="${HOME}/.zshrc"
  _marker='# eka shell completion'
  mkdir -p "$_zdir"
  # zsh completion files live in an fpath directory named `_eka`.
  "$INSTALL_DIR/eka" completion zsh > "$_zdir/_eka"
  if [ -f "$_rc" ] && grep -qF "$_marker" "$_rc" 2>/dev/null; then
    echo "    zsh: already registered in $_rc (skip)"
    return 0
  fi
  {
    printf '\n%s\n' "$_marker"
    # Quoted entry: HOME paths containing spaces must not break the
    # zsh array assignment.
    printf 'fpath=("%s" $fpath)\n' "$_zdir"
    # compinit is idempotent: re-running it after an existing setup is
    # safe, and autoload guards the missing-command case.
    printf '%s\n' 'autoload -Uz compinit && compinit'
  } >> "$_rc"
  echo "    zsh: registered in $_rc (reload: source ~/.zshrc)"
}

_register_fish() {
  _fdir="${HOME}/.config/fish/completions"
  mkdir -p "$_fdir"
  # fish auto-loads every file in completions/ — no rc edit needed.
  "$INSTALL_DIR/eka" completion fish > "$_fdir/eka.fish"
  echo "    fish: registered in $_fdir/eka.fish (auto-loaded)"
}

_setup_completion() {
  case "$1" in
    bash) _register_bash ;;
    zsh) _register_zsh ;;
    fish) _register_fish ;;
    none|"") echo "    skipped" ;;
  esac
}

echo ""
echo "  Shell completion:"
_shell="$(_resolve_completion_shell)"
if [ "$_shell" = "none" ]; then
  printf '    skipped (later: %s completion <bash|zsh|fish>)\n' "$INSTALL_DIR/eka"
else
  if _setup_completion "$_shell"; then
    printf '    ✓ registered %s completion\n' "$_shell"
  else
    printf '    ✗ could not register %s completion (install OK — run "%s completion %s" manually)\n' \
      "$_shell" "$INSTALL_DIR/eka" "$_shell"
  fi
fi

# ── Summary ────────────────────────────────────────────────────────
TOTAL_END=$(_now)
TOTAL_TIME=$(_elapsed "$TOTAL_START" "$TOTAL_END")

echo ""
echo "─────────────────"
echo "EKA CLI installed successfully!"
echo ""
echo "  Binary: $INSTALL_DIR/eka"
echo "  Time:   $TOTAL_TIME"
echo ""
echo "Run 'eka --help' to get started."
echo "Run 'eka init <name>' to create a new EKA repository."