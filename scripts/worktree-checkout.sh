#!/usr/bin/env bash
# worktree-checkout.sh — install to ~/bin/worktree-checkout.sh on server
# Usage: worktree-checkout.sh <branch> <repo-root> [post-setup-cmd]
# Checks out an existing remote branch (created by gh issue develop) into a new worktree.
# Does NOT create a new branch — use the repo's worktree.sh for that.
set -euo pipefail

BRANCH="$1"
REPO_ROOT="$(realpath "$2")"
POST_SETUP_CMD="${3:-}"
SANITIZED="${BRANCH//\//-}"
WORKTREE_DIR="$(dirname "$REPO_ROOT")/extractor-${SANITIZED}"
RESOURCES="$REPO_ROOT/resources"

[[ -d "$WORKTREE_DIR" ]] && { echo "Error: worktree already exists: $WORKTREE_DIR"; exit 1; }

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

mkdir -p "$WORKTREE_DIR/resources"
[[ -d "$RESOURCES/config" ]] && ln -s "$RESOURCES/config" "$WORKTREE_DIR/resources/config"
[[ -d "$RESOURCES/output" ]] && ln -s "$RESOURCES/output" "$WORKTREE_DIR/resources/output"
[[ -d "$RESOURCES/input/local" ]] && ln -s "$RESOURCES/input/local" "$WORKTREE_DIR/resources/input/local"
echo "Symlinked resources"

if [[ -d "$REPO_ROOT/node_modules" ]]; then
  echo "Copying node_modules (synchronous)..."
  cp -r "$REPO_ROOT/node_modules" "$WORKTREE_DIR/"
fi

if [[ -n "$POST_SETUP_CMD" ]]; then
  echo "Running post-setup: $POST_SETUP_CMD"
  cd "$WORKTREE_DIR"
  bash -ic "$POST_SETUP_CMD"
fi

echo ""
echo "=== Worktree ready ==="
echo "  Path:   $WORKTREE_DIR"
echo "  Branch: $BRANCH"
