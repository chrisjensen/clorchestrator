#!/usr/bin/env bash
# install.sh — run this on the SERVER to install server-side components
# For the local Mac scripts, see: local/clorchestrate.sh and local/tab.sh
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DISPATCH_DIR="$SCRIPT_DIR"

# Optional: install repo-specific skills into a target repo.
# Provide either REPO_ROOT env var or first argument to this script.
REPO_ROOT="${REPO_ROOT:-${1:-}}"

# Server scripts → ~/bin/
mkdir -p ~/bin
cp "$DISPATCH_DIR/server/start-task.sh" ~/bin/start-task.sh
cp "$DISPATCH_DIR/server/worktree-checkout.sh" ~/bin/worktree-checkout.sh
chmod +x ~/bin/start-task.sh ~/bin/worktree-checkout.sh
echo "Installed: ~/bin/start-task.sh"
echo "Installed: ~/bin/worktree-checkout.sh"

# install_skill_dir <src_skill_dir> <dest_parent>
# Copies a single skill directory (containing SKILL.md and optional supporting files)
# into <dest_parent>/<skill-name>/.
install_skill_dir() {
  local skill_dir="$1"
  local dest_parent="$2"
  local skill
  skill="$(basename "$skill_dir")"
  if [[ ! -f "$skill_dir/SKILL.md" ]]; then
    echo "WARNING: $skill_dir has no SKILL.md — skipping"
    return
  fi
  mkdir -p "$dest_parent/$skill"
  cp "$skill_dir/SKILL.md" "$dest_parent/$skill/SKILL.md"
  shopt -s nullglob
  local extras=("$skill_dir"/*)
  shopt -u nullglob
  local f
  for f in "${extras[@]}"; do
    [[ "$(basename "$f")" == "SKILL.md" ]] && continue
    cp -r "$f" "$dest_parent/$skill/"
  done
  echo "Installed: $dest_parent/$skill/"
}

# Global skills → ~/.claude/skills/<skill-name>/SKILL.md
GLOBAL_SRC_DIR="$DISPATCH_DIR/skills/global"
GLOBAL_DEST_DIR="$HOME/.claude/skills"
mkdir -p "$GLOBAL_DEST_DIR"

if [[ -d "$GLOBAL_SRC_DIR" ]]; then
  while IFS= read -r skill_dir; do
    install_skill_dir "$skill_dir" "$GLOBAL_DEST_DIR"
  done < <(find "$GLOBAL_SRC_DIR" -mindepth 1 -maxdepth 1 -type d | sort)
else
  echo "WARNING: $GLOBAL_SRC_DIR not found — global skills not installed."
fi

# Repo-specific skills → <repo>/.claude/skills/<skill-name>/SKILL.md
REPO_SKILLS_SRC_DIR="$DISPATCH_DIR/skills/repo"
if [[ -n "${REPO_ROOT}" ]]; then
  if [[ -d "$REPO_ROOT" ]]; then
    REPO_SKILLS_DEST_DIR="$REPO_ROOT/.claude/skills"
    mkdir -p "$REPO_SKILLS_DEST_DIR"

    if [[ -d "$REPO_SKILLS_SRC_DIR" ]]; then
      while IFS= read -r skill_dir; do
        install_skill_dir "$skill_dir" "$REPO_SKILLS_DEST_DIR"
      done < <(find "$REPO_SKILLS_SRC_DIR" -mindepth 1 -maxdepth 1 -type d | sort)
    else
      echo "WARNING: $REPO_SKILLS_SRC_DIR not found — repo skills not installed."
    fi
  else
    echo "WARNING: REPO_ROOT '$REPO_ROOT' is not a directory — repo skills not installed."
  fi
else
  echo "Note: REPO_ROOT not provided — skipping repo-specific skills install."
  echo "  Usage: REPO_ROOT=/path/to/repo ./install.sh"
  echo "     or: ./install.sh /path/to/repo"
fi

echo ""
echo "=== Server install complete ==="
echo ""
echo "Still to do on your local Mac:"
echo "  mkdir -p ~/bin"
echo "  cp $DISPATCH_DIR/local/clorchestrate.sh ~/bin/clorchestrate.sh"
echo "  cp $DISPATCH_DIR/local/tab.sh ~/bin/tab.sh"
echo "  chmod +x ~/bin/clorchestrate.sh ~/bin/tab.sh"
echo ""
echo "And create your config:"
echo "  mkdir -p ~/.dispatch"
echo "  cp $DISPATCH_DIR/config/bitmark-extractor-ai.conf.example ~/.dispatch/bitmark-extractor-ai.conf"
echo "  # Edit ~/.dispatch/bitmark-extractor-ai.conf with real values"
echo ""
echo "Verify gh issue develop output format before first run:"
echo "  gh issue develop --list 1 --repo getMoreBrain/cosmic"
