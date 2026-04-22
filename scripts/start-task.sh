#!/usr/bin/env bash
# start-task.sh — install to ~/bin/start-task.sh on server
# Usage: start-task.sh <handle>
# Expects /tmp/task-<handle>.conf to have been written by tab.sh (the local launcher)
set -euo pipefail

HANDLE="$1"
CONF="/tmp/task-${HANDLE}.conf"
[[ ! -f "$CONF" ]] && { echo "No task config found at $CONF"; exit 1; }
source "$CONF"

SANITIZED="${BRANCH//\//-}"
WORKTREE_DIR="$(dirname "$(realpath "$REMOTE_REPO")")/extractor-${SANITIZED}"

cd "$REMOTE_REPO"
git fetch origin
~/bin/worktree-checkout.sh "$BRANCH" "$REMOTE_REPO" "${POST_SETUP_CMD:-}"
cd "$WORKTREE_DIR"

if [[ -n "${ISSUE:-}" ]]; then
  ISSUE_URL="https://github.com/${ISSUE_REPO}/issues/${ISSUE}"
  PROMPT="You are implementing GitHub issue #${ISSUE} in ${ISSUE_REPO}.

## Repo context
${PLANNING_CONTEXT}

## Issue + discussion (must review first)
Open ${ISSUE_URL} and review:
- the issue description
- all comments/discussion (including any linked PRs, decisions, edge cases, and constraints)

## Additional context
${EXTRA_CONTEXT}
"
  exec claude "$PROMPT"
else
  exec claude
fi
