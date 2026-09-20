#!/usr/bin/env bash
# VENDORED from ~/src/claude-helpers/bin/hive-signal.sh — that is the source of
# truth; this copy is embedded so it can be shipped to remote servers. Keep in
# sync when the canonical version changes.
#
# hive-signal.sh — filesystem signalling for a hive run (see the hive-worker /
# hive-coordinate skills). N single-provider Claude Code sessions coordinate only
# through files in a run dir; this is the emit/wait primitive they share.
#
# Signals are bare files in <coord_dir> itself: a sentinel's *existence* means "done" —
# no payload, so there's no JSON to malform and nothing to collide on. Sentinels are
# files and worktrees are subdirs, so the two never clash.
#
# The label set is the worker subdirectories of <coord_dir> — each worker runs in a
# worktree named for its label (dotdirs are excluded by the glob). No manifest, no
# JSON: pure filesystem. Stays N-agnostic.
#
# Usage:
#   hive-signal.sh emit <coord_dir> <label> <stage>
#   hive-signal.sh merged <coord_dir>
#   hive-signal.sh wait <coord_dir> <stage>          # block until ALL labels done
#   hive-signal.sh wait-one <coord_dir> <signal>     # block until one sentinel exists
#
# Blocks via inotifywait when available (install inotify-tools); falls back to
# polling. Fails loud on bad args or an empty run dir.
set -eu

have() { command -v "$1" >/dev/null 2>&1; }

die() { echo "hive-signal: $*" >&2; exit 2; }

# Worker labels = the subdirectories of <coord_dir>, one per line. (Leading-dot dirs
# never match the glob; sentinels are files, so they're never listed.)
labels_of() {
  local coord="$1"
  [ -d "$coord" ] || die "no run dir at $coord"
  local d found=
  for d in "$coord"/*/; do
    [ -d "$d" ] || continue          # no match -> literal glob, skip
    printf '%s\n' "$(basename "$d")"
    found=1
  done
  [ -n "$found" ] || die "no worker subdirs in $coord"
}

cmd_emit() {
  local coord="${1:-}" label="${2:-}" stage="${3:-}"
  [ -n "$coord" ] && [ -n "$label" ] && [ -n "$stage" ] || die "usage: emit <coord_dir> <label> <stage>"
  : > "$coord/$label.$stage.done"
  echo "hive-signal: emitted $label.$stage.done"
}

cmd_merged() {
  local coord="${1:-}"
  [ -n "$coord" ] || die "usage: merged <coord_dir>"
  : > "$coord/plan.merged"
  echo "hive-signal: emitted plan.merged"
}

# Block until every file in $@ exists. Check-first, then wait for a create event in
# <coord_dir> (or a short timeout) and re-check — the timeout closes the race where a
# file appears between the check and arming the watch. The watch is non-recursive, so
# churn inside the worktree subdirs never wakes it.
wait_for() {
  local coord="$1"; shift
  local -a targets=("$@")
  all_present() { local f; for f in "${targets[@]}"; do [ -f "$f" ] || return 1; done; return 0; }
  while ! all_present; do
    if have inotifywait; then
      inotifywait -q -t 5 -e create,moved_to,close_write "$coord" >/dev/null 2>&1 || true
    else
      sleep 1
    fi
  done
}

cmd_wait() {
  local coord="${1:-}" stage="${2:-}"
  [ -n "$coord" ] && [ -n "$stage" ] || die "usage: wait <coord_dir> <stage>"
  # Read labels eagerly so a labels_of error (die in a subshell) is not swallowed by
  # the here-string below, which would leave targets empty and let the wait succeed
  # vacuously.
  local labels; labels="$(labels_of "$coord")"
  local -a targets=()
  local label
  while IFS= read -r label; do [ -n "$label" ] && targets+=("$coord/$label.$stage.done"); done <<< "$labels"
  [ "${#targets[@]}" -gt 0 ] || die "no worker subdirs in $coord"
  wait_for "$coord" "${targets[@]}"
  echo "hive-signal: all '$stage' signals present"
}

cmd_wait_one() {
  local coord="${1:-}" signal="${2:-}"
  [ -n "$coord" ] && [ -n "$signal" ] || die "usage: wait-one <coord_dir> <signal>"
  wait_for "$coord" "$coord/$signal"
  echo "hive-signal: '$signal' present"
}

sub="${1:-}"; shift || true
case "$sub" in
  emit)     cmd_emit "$@" ;;
  merged)   cmd_merged "$@" ;;
  wait)     cmd_wait "$@" ;;
  wait-one) cmd_wait_one "$@" ;;
  *) die "unknown subcommand '${sub:-}'. Use: emit | merged | wait | wait-one" ;;
esac
