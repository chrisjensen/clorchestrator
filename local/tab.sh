#!/usr/bin/env bash
# tab.sh — install to ~/bin/tab.sh on local Mac
# Launches a single iTerm2 tab/session; three modes depending on args.
set -euo pipefail

usage() {
  cat <<'EOF'
tab.sh <config>                                               # Mode 3: bare ssh session in $DISPATCH_REMOTE_REPO
tab.sh <config> <handle> <branch>                             # Mode 2: worktree + claude (no prompt)
tab.sh <config> <handle> <branch> \
    --issue <N|url> [--extra-context <text>]                  # Mode 1: full task (worktree + claude w/ prompt)

--issue accepts a bare number, "#NNN", "org/repo#NNN", or a full GitHub issue URL.
EOF
}

help() {
  usage
  cat <<'EOF'

Modes:
  Mode 1 (task):         Writes /tmp/task-<handle>.conf on the server, opens an iTerm2
                         tab that ssh's in and runs `~/bin/start-task.sh <handle>` inside
                         a screen session. This is how clorchestrate.sh calls the launcher.
  Mode 2 (worktree):     Same flow as Mode 1 but with no issue/prompt context. start-task.sh
                         sees no ISSUE and launches `claude` without a planning prompt.
  Mode 3 (bare session): Opens an iTerm2 tab that ssh's to $DISPATCH_SERVER and cd's to
                         $DISPATCH_REMOTE_REPO. No worktree, no screen, no Claude.

The iTerm2 tab color (if DISPATCH_ITERM_TAB_COLOR is set in the config) is applied in
all modes.
EOF
}

if [[ $# -eq 0 ]]; then
  usage
  exit 0
fi

case "${1:-}" in
  --help|-h)
    help
    exit 0
    ;;
esac

CONFIG="$1"
shift
source "$CONFIG"

HANDLE=""
BRANCH=""
ISSUE=""
EXTRA_CONTEXT=""

# Positional args: up to two (handle, branch) before flags.
if [[ $# -gt 0 && "${1:0:2}" != "--" ]]; then
  HANDLE="$1"
  shift
fi
if [[ $# -gt 0 && "${1:0:2}" != "--" ]]; then
  BRANCH="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --issue)
      ISSUE="$2"; shift 2 ;;
    --extra-context)
      EXTRA_CONTEXT="$2"; shift 2 ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2 ;;
  esac
done

# Normalize --issue: accept a bare number, "#NNN", "org/repo#NNN", or a full
# GitHub issue URL. Store just the number.
if [[ -n "$ISSUE" ]]; then
  issue_norm=$(echo "$ISSUE" | grep -oE 'https?://github\.com/[^/[:space:]]+/[^/[:space:]]+/issues/[0-9]+|([A-Za-z0-9_-]+/[A-Za-z0-9_-]+)?#?[0-9]+' | head -1 | grep -oE '[0-9]+$')
  [[ -z "$issue_norm" ]] && { echo "Could not parse issue from --issue='$ISSUE'" >&2; exit 2; }
  ISSUE="$issue_norm"
fi

# Mode detection
if [[ -z "$HANDLE" && -z "$BRANCH" ]]; then
  MODE=3
elif [[ -n "$HANDLE" && -n "$BRANCH" && -z "$ISSUE" ]]; then
  MODE=2
elif [[ -n "$HANDLE" && -n "$BRANCH" && -n "$ISSUE" ]]; then
  MODE=1
else
  echo "Invalid argument combination." >&2
  usage >&2
  exit 2
fi

# Optional iTerm2 tab color (hex: #RRGGBB or RRGGBB). Implemented via iTerm2 escape codes.
tab_color_cmd=""
if [[ -n "${DISPATCH_ITERM_TAB_COLOR:-}" ]]; then
  hex="${DISPATCH_ITERM_TAB_COLOR#\#}"
  if [[ "$hex" =~ ^[0-9A-Fa-f]{6}$ ]]; then
    r=$((16#${hex:0:2}))
    g=$((16#${hex:2:2}))
    b=$((16#${hex:4:2}))
    tab_color_cmd="printf '\\033]6;1;bg;red;brightness;${r}\\a\\033]6;1;bg;green;brightness;${g}\\a\\033]6;1;bg;blue;brightness;${b}\\a'; "
  else
    echo "Invalid DISPATCH_ITERM_TAB_COLOR='$DISPATCH_ITERM_TAB_COLOR' (expected #RRGGBB), skipping tab color" >&2
  fi
fi

# Modes 1 & 2: write task config on server so start-task.sh can source it.
if [[ "$MODE" != "3" ]]; then
  if [[ "$MODE" == "1" ]]; then
    ssh "$DISPATCH_SERVER" "cat > /tmp/task-${HANDLE}.conf" <<CONF
ISSUE=$ISSUE
BRANCH=$BRANCH
REMOTE_REPO=$DISPATCH_REMOTE_REPO
ISSUE_REPO=$DISPATCH_ISSUE_REPO
POST_SETUP_CMD=$(printf '%q' "${DISPATCH_POST_SETUP_CMD:-}")
PLANNING_CONTEXT=$(printf '%q' "$DISPATCH_PLANNING_CONTEXT")
EXTRA_CONTEXT=$(printf '%q' "$EXTRA_CONTEXT")
CONF
  else
    # Mode 2: no ISSUE/ISSUE_REPO/PLANNING_CONTEXT/EXTRA_CONTEXT
    ssh "$DISPATCH_SERVER" "cat > /tmp/task-${HANDLE}.conf" <<CONF
BRANCH=$BRANCH
REMOTE_REPO=$DISPATCH_REMOTE_REPO
POST_SETUP_CMD=$(printf '%q' "${DISPATCH_POST_SETUP_CMD:-}")
CONF
  fi
fi

# Build the command the iTerm2 tab runs.
case "$MODE" in
  1|2)
    remote_cmd="ssh -t $DISPATCH_SERVER \"screen -S $HANDLE bash -c '~/bin/start-task.sh $HANDLE; exec bash'\""
    ;;
  3)
    remote_cmd="ssh -t $DISPATCH_SERVER \"cd $DISPATCH_REMOTE_REPO && exec bash -l\""
    ;;
esac

osascript <<EOF
tell application "iTerm2"
  tell current window
    create tab with default profile
    tell current session of current tab
      write text "${tab_color_cmd}${remote_cmd}"
    end tell
  end tell
end tell
EOF
