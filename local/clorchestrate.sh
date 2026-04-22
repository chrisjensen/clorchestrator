#!/usr/bin/env bash
# clorchestrate.sh — install to ~/bin/clorchestrate.sh on local Mac
# Iterates a tasks.md file, creates a branch per entry via `gh issue develop`,
# then hands off to tab.sh (expected in the same directory) to open one iTerm2
# tab per task.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LAUNCHER="$SCRIPT_DIR/tab.sh"

usage() {
  echo "clorchestrate.sh <config> <input>"
}

help() {
  usage
  cat <<'EOF'

Input file format:
  - The file is Markdown.
  - Each entry starts with a heading: "## <handle>"
  - Lines following the heading are the entry details until the next "## ...".
  - The details must include an issue reference. Any of these work:
      #NNN
      org/repo#NNN
      https://github.com/org/repo/issues/NNN
  - Optional: base branch override line: "base: <ref>"

Example:
  ## my-task
  https://github.com/org/repo/issues/123
  base: main
  Some extra context lines...
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

if [[ $# -ne 2 ]]; then
  usage >&2
  exit 2
fi

CONFIG="$1"
INPUT="$2"
source "$CONFIG"

[[ -x "$LAUNCHER" ]] || { echo "Launcher not found or not executable: $LAUNCHER" >&2; exit 1; }

current_handle=""
current_details=""

process_entry() {
  local handle="$1" details="$2"
  [[ -z "$handle" ]] && return

  # Extract issue number from details. Accepts:
  #   #NNN                                     (bare)
  #   org/repo#NNN                             (shorthand)
  #   https://github.com/org/repo/issues/NNN   (full URL)
  local issue_num
  issue_num=$(echo "$details" | grep -oE 'https?://github\.com/[^/[:space:]]+/[^/[:space:]]+/issues/[0-9]+|([A-Za-z0-9_-]+/[A-Za-z0-9_-]+)?#[0-9]+' | head -1 | grep -oE '[0-9]+$')
  [[ -z "$issue_num" ]] && { echo "No issue ref found for '$handle', skipping"; return; }

  # Extract optional base branch override (line: "base: <ref>")
  local base_branch
  base_branch=$(echo "$details" | grep -E '^base:' | sed 's/^base:[[:space:]]*//' | tr -d '\n')
  base_branch="${base_branch:-${DISPATCH_DEFAULT_BASE}}"

  # Strip base: line before passing context to planner
  local extra_context
  extra_context=$(echo "$details" | grep -v '^base:')

  echo "Creating branch for '$handle' (#$issue_num from $base_branch)..."

  # Build gh issue develop args
  local gh_args
  gh_args=(issue develop "$issue_num"
    --repo "$DISPATCH_ISSUE_REPO"
    --branch-repo "$DISPATCH_BRANCH_REPO"
    --base "$base_branch")

  if [[ -n "${DISPATCH_BRANCH_NAME_FORMAT:-}" ]]; then
    local branch_name="${DISPATCH_BRANCH_NAME_FORMAT//\{issue\}/$issue_num}"
    branch_name="${branch_name//\{handle\}/$handle}"
    gh_args+=(--name "$branch_name")
  fi

  local raw branch
  raw=$(gh "${gh_args[@]}" 2>&1)
  branch=$(echo "$raw" | grep -oE '[^/]+$' | tail -1)
  [[ -z "$branch" ]] && { echo "Could not parse branch name from: $raw"; exit 1; }
  echo "  Branch: $branch"

  "$LAUNCHER" "$CONFIG" "$handle" "$branch" \
    --issue "$issue_num" \
    --extra-context "$extra_context"
}

while IFS= read -r line; do
  if [[ "$line" =~ ^##[[:space:]]+(.+)$ ]]; then
    process_entry "$current_handle" "$current_details"
    current_handle="${BASH_REMATCH[1]}"
    current_details=""
  else
    current_details+="$line"$'\n'
  fi
done < "$INPUT"
process_entry "$current_handle" "$current_details"
