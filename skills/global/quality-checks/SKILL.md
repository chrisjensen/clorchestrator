---
description: Run code quality checks: linter, prettier, git hooks, etc
---

Check all quality gates present in this repo. For each of the following that is configured, run it and fix all errors before proceeding to the next:

Examine the repo configuration for any standard commands created to do quality checks

If Github Actions, CircleCI or similar configurations exist in the repo, examine them to identify examples of quality control commands that should be run.

Examples of such commands could include
- TypeScript: if tsconfig.json exists, run the repo's typecheck script or `npx tsc --noEmit`
- Linter
- Prettier
- Git hooks
- Test

Do not proceed past any failures. Fix errors at each step before moving on.
