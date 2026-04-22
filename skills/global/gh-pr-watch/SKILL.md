---
description: background process to wait for PR checks to pass
argument-hint: "[pr-number-or-url]"
arguments: [pr]
model: haiku
effort: low
context: fork
---

Run the following command to wait for PR checks to finish.
Report the output of this to your caller

```!bash
tmp="$(mktemp -t gh-pr-checks.XXXXXX.log)"

pr_ref=""
if [ -n "${pr:-}" ]; then
  pr_ref="$pr"
fi

# If a PR ref is provided as the skill argument, watch that PR explicitly;
# otherwise default to the PR associated with the current branch (gh's default behavior).
if gh pr checks ${pr_ref:+ "$pr_ref"} --watch --fail-fast >"$tmp" 2>&1; then
  echo "Checks passed"
  exit 0
else
  echo "Checks failed"
  cat "$tmp" >&2
  echo "Full logs in $tmp" >&2
  exit 1
fi
```
