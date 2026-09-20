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

# Hive mode (multi-session benchmark run): RUN_DIR is a plain directory holding
# task.md, the coordination sentinels, and one worktree subdir per worker label.
# When set, prepare_run_dir creates it and stages task.md from /tmp/task-<h>.md.
RUN_DIR="${RUN_DIR:-}"
TASK_MD_SRC="/tmp/task-${HANDLE}.md"
prepare_run_dir() {
  [[ -z "$RUN_DIR" ]] && return
  mkdir -p "$RUN_DIR"
  if [[ -f "$TASK_MD_SRC" ]]; then
    cp "$TASK_MD_SRC" "$RUN_DIR/task.md"
    echo "Wrote $RUN_DIR/task.md"
  fi
}

# Coordinator: RUN_DIR but no worktree of its own — just prepare the run dir.
if [[ -n "$RUN_DIR" && -z "${WORKTREE_DIR:-}" ]]; then
  echo "=== Coordinator run-dir setup ==="
  echo "  Run dir: $RUN_DIR"
  prepare_run_dir
  exit 0
fi

REPO_ROOT="$(realpath "$REMOTE_REPO")"
SANITIZED="${BRANCH//\//-}"
WORKTREE_PREFIX="${WORKTREE_PREFIX:-$(basename "$REPO_ROOT")}"
# In hive mode WORKTREE_DIR is passed explicitly (a child of RUN_DIR); otherwise
# derive the sibling-of-repo path.
WORKTREE_DIR="${WORKTREE_DIR:-$(dirname "$REPO_ROOT")/${WORKTREE_PREFIX}-${SANITIZED}}"
RESOURCES="$REPO_ROOT/resources"
POST_SETUP_CMD="${POST_SETUP_CMD:-}"

# Ensure the run dir (worktree parent, in hive mode) exists before git creates
# the worktree, and stage task.md there.
prepare_run_dir
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
  if [[ -d "$WORKTREE_DIR/.serena" ]]; then
    echo "  serena: present"
  else
    echo "  serena: not found"
  fi
  start_background_setup
  exit 0
fi

# --- Fresh setup path ---
cd "$REPO_ROOT"
if git ls-remote --exit-code origin "$BRANCH" > /dev/null 2>&1; then
  git fetch origin "$BRANCH"
fi

# Use bd worktree create only for stealth beads (untracked .beads dir).
# If .beads is committed on the branch, plain git worktree add preserves it;
# bd worktree create would overwrite it with a redirect to the main repo's DB.
BEADS_COMMITTED=false
if [[ -d "$REPO_ROOT/.beads" ]] && git -C "$REPO_ROOT" ls-files --error-unmatch .beads/ > /dev/null 2>&1; then
  BEADS_COMMITTED=true
fi

# Other worktrees of this repo may have concurrently-running Claude sessions
# that write to the shared .git/config (e.g. `git push -u`) at any time. That
# can collide with the config writes below (git branch --track, git worktree
# add ... origin/$BRANCH) and fail with "could not lock config file
# .git/config: File exists". This is transient, so retry a few times.
create_worktree() {
  # Drop stale worktree registrations so a worktree dir that was deleted
  # without `git worktree remove` doesn't block re-adding it here.
  git -C "$REPO_ROOT" worktree prune

  echo "Checking out $BRANCH into $WORKTREE_DIR..."
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
}

MAX_ATTEMPTS=3
attempt=1
until create_worktree; do
  if [[ $attempt -ge $MAX_ATTEMPTS ]]; then
    echo "error: worktree setup failed after $MAX_ATTEMPTS attempts" >&2
    exit 1
  fi
  echo "worktree setup failed (attempt $attempt/$MAX_ATTEMPTS) — retrying in 5s..." >&2
  sleep 5
  attempt=$((attempt + 1))
done

SERENA_SRC="$(git -C "$REPO_ROOT" worktree list --porcelain | awk '/^worktree /{print $2; exit}')"
if [[ -d "$SERENA_SRC/.serena" ]]; then
  # Copy .serena from main worktree, excluding cache/ (LSP index rebuilds automatically).
  rsync -a --exclude='cache/' "$SERENA_SRC/.serena/" "$WORKTREE_DIR/.serena/"
  # Update project_name in project.local.yml to match the new worktree directory name.
  local_yml="$WORKTREE_DIR/.serena/project.local.yml"
  if [[ -f "$local_yml" ]]; then
    WORKTREE_NAME="$(basename "$WORKTREE_DIR")"
    sed -i "s|^project_name:.*|project_name: \"$WORKTREE_NAME\"|" "$local_yml"
  fi
  echo "Seeded .serena from $SERENA_SRC (cache excluded)"
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
