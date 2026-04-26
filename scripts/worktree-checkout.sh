#!/usr/bin/env bash
# worktree-checkout.sh — install to ~/bin/worktree-checkout.sh on server
# Usage: worktree-checkout.sh <handle>
# Sources /tmp/task-<handle>.conf for BRANCH / REMOTE_REPO / POST_SETUP_CMD.
# Does essential setup synchronously, then kicks off heavy work (node_modules
# copy, post-setup build) into a detached background job logged to
# <worktree>/.setup.log. Intended to be run via plain (non-tty) SSH.
set -euo pipefail

HANDLE="$1"
CONF="/tmp/task-${HANDLE}.conf"
[[ ! -f "$CONF" ]] && { echo "No task config found at $CONF"; exit 1; }
# shellcheck disable=SC1090
source "$CONF"

REPO_ROOT="$(realpath "$REMOTE_REPO")"
SANITIZED="${BRANCH//\//-}"
WORKTREE_DIR="$(dirname "$REPO_ROOT")/extractor-${SANITIZED}"
RESOURCES="$REPO_ROOT/resources"
POST_SETUP_CMD="${POST_SETUP_CMD:-}"

start_background_setup() {
  if [[ ! -d "$REPO_ROOT/node_modules" ]] && [[ -z "$POST_SETUP_CMD" ]]; then
    return
  fi
  echo "Background setup starting: tail -f $WORKTREE_DIR/.setup.log"
  (
    cd "$WORKTREE_DIR"
    if [[ -d "$REPO_ROOT/node_modules" ]] && [[ ! -d "$WORKTREE_DIR/node_modules" ]]; then
      echo "Copying node_modules..."
      cp -r "$REPO_ROOT/node_modules" "$WORKTREE_DIR/"
    fi
    if [[ -n "$POST_SETUP_CMD" ]]; then
      echo "Running post-setup: $POST_SETUP_CMD"
      bash -lc "$POST_SETUP_CMD"
    fi
    echo "=== Background setup complete ==="
  ) > "$WORKTREE_DIR/.setup.log" 2>&1 &
  disown
}

# --- Resume path: worktree already exists ---
if [[ -d "$WORKTREE_DIR" ]]; then
  echo "=== Resuming existing worktree ==="
  echo "  Path:   $WORKTREE_DIR"
  echo "  Branch: $BRANCH"
  start_background_setup
  exit 0
fi

# --- Fresh setup path ---
cd "$REPO_ROOT"
git fetch origin

echo "Checking out $BRANCH into $WORKTREE_DIR..."
git -C "$REPO_ROOT" worktree add "$WORKTREE_DIR" "$BRANCH"

if [[ -d "$REPO_ROOT/.claude" ]]; then
  cp -r "$REPO_ROOT/.claude" "$WORKTREE_DIR/.claude"
  echo "Copied .claude/ (skills, settings)"
fi

for doc_file in CLAUDE.md AGENTS.md; do
  if [[ -f "$REPO_ROOT/$doc_file" ]] && [[ ! -f "$WORKTREE_DIR/$doc_file" ]]; then
    cp "$REPO_ROOT/$doc_file" "$WORKTREE_DIR/$doc_file"
    echo "Copied $doc_file"
  fi
done

[[ -f "$REPO_ROOT/.env" ]] && cp "$REPO_ROOT/.env" "$WORKTREE_DIR/.env" && echo "Copied .env"

mkdir -p "$WORKTREE_DIR/resources/input"
[[ -d "$RESOURCES/config" ]] && ln -s "$RESOURCES/config" "$WORKTREE_DIR/resources/config"
[[ -d "$RESOURCES/input/local" ]] && ln -s "$RESOURCES/input/local" "$WORKTREE_DIR/resources/input/local"
[[ -d "$RESOURCES/input/my" ]] && ln -s "$RESOURCES/input/my" "$WORKTREE_DIR/resources/input/my"
echo "Symlinked resources"

echo ""
echo "=== Worktree ready ==="
echo "  Path:   $WORKTREE_DIR"
echo "  Branch: $BRANCH"

start_background_setup
