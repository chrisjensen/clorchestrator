#!/usr/bin/env bash
# worktree-checkout.sh — install to ~/bin/worktree-checkout.sh on server
# Usage: worktree-checkout.sh <handle>
# Sources /tmp/task-<handle>.conf for BRANCH / REMOTE_REPO / POST_SETUP_CMD.
# Does essential setup synchronously, then kicks off heavy work (node_modules
# symlink, post-setup build) into a detached background job logged to
# <worktree>/.setup.log. Intended to be run via plain (non-tty) SSH.
set -euo pipefail

HANDLE="$1"
CONF="/tmp/task-${HANDLE}.conf"
[[ ! -f "$CONF" ]] && { echo "No task config found at $CONF"; exit 1; }
# shellcheck disable=SC1090
source "$CONF"

REPO_ROOT="$(realpath "$REMOTE_REPO")"
SANITIZED="${BRANCH//\//-}"
WORKTREE_PREFIX="${WORKTREE_PREFIX:-$(basename "$REPO_ROOT")}"
WORKTREE_DIR="$(dirname "$REPO_ROOT")/${WORKTREE_PREFIX}-${SANITIZED}"
RESOURCES="$REPO_ROOT/resources"
POST_SETUP_CMD="${POST_SETUP_CMD:-}"
start_background_setup() {
  if [[ ! -d "$REPO_ROOT/node_modules" ]] && [[ -z "$POST_SETUP_CMD" ]]; then
    return
  fi
  echo "Background setup starting: tail -f $WORKTREE_DIR/.setup.log"
  (
    cd "$WORKTREE_DIR"
    if [[ -d "$REPO_ROOT/node_modules" ]] && [[ ! -e "$WORKTREE_DIR/node_modules" ]]; then
      echo "Symlinking node_modules..."
      ln -s "$REPO_ROOT/node_modules" "$WORKTREE_DIR/node_modules"
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
  if [[ -d "$WORKTREE_DIR/.tokensave" ]]; then
    echo "  tokensave: present"
  else
    echo "  tokensave: not found"
  fi
  start_background_setup
  exit 0
fi

# --- Fresh setup path ---
cd "$REPO_ROOT"
git fetch origin

# Drop stale worktree registrations so a worktree dir that was deleted without
# `git worktree remove` doesn't block re-adding it here.
git -C "$REPO_ROOT" worktree prune

echo "Checking out $BRANCH into $WORKTREE_DIR..."
# Use bd worktree create only for stealth beads (untracked .beads dir).
# If .beads is committed on the branch, plain git worktree add preserves it;
# bd worktree create would overwrite it with a redirect to the main repo's DB.
BEADS_COMMITTED=false
if [[ -d "$REPO_ROOT/.beads" ]] && git -C "$REPO_ROOT" ls-files --error-unmatch .beads/ > /dev/null 2>&1; then
  BEADS_COMMITTED=true
fi

if [[ -d "$REPO_ROOT/.beads" ]] && [[ "$BEADS_COMMITTED" == false ]]; then
  # Stealth beads repo: use `bd worktree create` so the new worktree shares
  # the main repo's .beads database via a redirect. bd attaches an existing
  # local branch but does not create one tracking origin, so set that up first
  # to preserve the resume-a-remote-branch behaviour of the plain-git path below.
  if ! git -C "$REPO_ROOT" show-ref --verify --quiet "refs/heads/$BRANCH"; then
    if git -C "$REPO_ROOT" show-ref --verify --quiet "refs/remotes/origin/$BRANCH"; then
      git -C "$REPO_ROOT" branch --track "$BRANCH" "origin/$BRANCH"
    fi
    # else: bd creates a new branch from the current HEAD
  fi
  # bd lives on the login-shell PATH (like npm/claude), not the bare PATH this
  # script gets over non-tty SSH — run it via `bash -lc` so it resolves.
  ( cd "$REPO_ROOT" && WT="$WORKTREE_DIR" BR="$BRANCH" \
      bash -lc 'bd worktree create "$WT" --branch="$BR"' )
elif git -C "$REPO_ROOT" show-ref --verify --quiet "refs/heads/$BRANCH"; then
  # Local branch already exists (e.g. a prior run created it) — attach it
  # rather than trying to recreate it, which would fail.
  git -C "$REPO_ROOT" worktree add "$WORKTREE_DIR" "$BRANCH"
elif git -C "$REPO_ROOT" show-ref --verify --quiet "refs/remotes/origin/$BRANCH"; then
  git -C "$REPO_ROOT" worktree add "$WORKTREE_DIR" -b "$BRANCH" "origin/$BRANCH"
else
  git -C "$REPO_ROOT" worktree add -b "$BRANCH" "$WORKTREE_DIR"
fi

TOKENSAVE_SRC="$(git -C "$REPO_ROOT" worktree list --porcelain | awk '/^worktree /{print $2; exit}')"
if [[ -d "$TOKENSAVE_SRC/.tokensave" ]]; then
  echo "Updating tokensave on main worktree before seeding..."

  DEFAULT_BRANCH=$(git -C "$TOKENSAVE_SRC" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|.*/||')
  DEFAULT_BRANCH="${DEFAULT_BRANCH:-main}"

  # Fetch all refs non-fatally; a missing/unreachable branch must not kill setup.
  git -C "$TOKENSAVE_SRC" fetch origin --quiet 2>/dev/null || echo "  warning: git fetch failed, continuing with local state"

  CURRENT_BRANCH=$(git -C "$TOKENSAVE_SRC" symbolic-ref --short HEAD 2>/dev/null || true)
  NEEDS_CHECKOUT=false
  if [[ "$CURRENT_BRANCH" != "$DEFAULT_BRANCH" ]]; then NEEDS_CHECKOUT=true; fi

  NEEDS_PULL=false
  if [[ "$(git -C "$TOKENSAVE_SRC" rev-parse HEAD 2>/dev/null || true)" != "$(git -C "$TOKENSAVE_SRC" rev-parse "origin/$DEFAULT_BRANCH" 2>/dev/null || true)" ]]; then
    NEEDS_PULL=true
  fi

  STASHED=false
  if [[ "$NEEDS_CHECKOUT" == true ]] || [[ "$NEEDS_PULL" == true ]]; then
    if ! git -C "$TOKENSAVE_SRC" diff --quiet || ! git -C "$TOKENSAVE_SRC" diff --cached --quiet; then
      git -C "$TOKENSAVE_SRC" stash push -m "clorchestrate-tokensave-update"
      STASHED=true
    fi
    if [[ "$NEEDS_CHECKOUT" == true ]]; then git -C "$TOKENSAVE_SRC" checkout "$DEFAULT_BRANCH"; fi
    if [[ "$NEEDS_PULL" == true ]]; then git -C "$TOKENSAVE_SRC" pull origin "$DEFAULT_BRANCH" --ff-only; fi
  fi

  bash -lc "tokensave sync \"$TOKENSAVE_SRC\"" || echo "  warning: tokensave sync failed, continuing"

  if [[ "$NEEDS_CHECKOUT" == true ]]; then git -C "$TOKENSAVE_SRC" checkout "$CURRENT_BRANCH"; fi
  if [[ "$STASHED" == true ]]; then git -C "$TOKENSAVE_SRC" stash pop; fi

  cp -a "$TOKENSAVE_SRC/.tokensave" "$WORKTREE_DIR/.tokensave"
  tmp="$WORKTREE_DIR/.tokensave/config.json"
  if [[ -f "$tmp" ]]; then
    if command -v jq &>/dev/null; then
      jq --arg r "$WORKTREE_DIR" '.root_dir=$r' "$tmp" > "$tmp.new" && mv "$tmp.new" "$tmp"
    else
      sed -i "s|\"root_dir\":\"[^\"]*\"|\"root_dir\":\"$WORKTREE_DIR\"|" "$tmp"
    fi
  fi
  echo "Seeded .tokensave from $TOKENSAVE_SRC (pre-synced)"
fi

if [[ -d "$REPO_ROOT/.claude" ]]; then
  mkdir -p "$WORKTREE_DIR/.claude"
  find "$REPO_ROOT/.claude" -mindepth 1 -type d | while read -r dir; do
    mkdir -p "$WORKTREE_DIR/${dir#"$REPO_ROOT"/}"
  done
  find "$REPO_ROOT/.claude" -type f -size +0c | while read -r f; do
    cp "$f" "$WORKTREE_DIR/${f#"$REPO_ROOT"/}"
  done
  if [[ -z "$(find "$WORKTREE_DIR/.claude" -type f 2>/dev/null)" ]]; then
    rm -rf "$WORKTREE_DIR/.claude"
    echo "Skipped .claude/ (no non-empty files)"
  else
    echo "Copied .claude/ (skills, settings)"
  fi
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
