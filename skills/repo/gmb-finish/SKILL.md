---
description: Steps to finish a PR once the code is written: code reviiew, quality checks, tests and submit a PR
---

Create a task list with the following items, work through any issues discovered.
Do not start the next task until the current one completes successfully.

Solve what you can, but if you hit major issues that would change the approach agreed in the plan, surface proposed actions to the user for review before acting on them.

1. Quality checks
Run the following tasks in subagents, verify if the issues are correctly identified and important to solve before releasing the code.

- /quality-correctness
- /quality-checks
- /quality-consistency
- /quality-dry
- /quality-review

If there are competing recommendations, prioritise:
- correctness
- alignment with existing architecture
- ease of understanding for a developer

Once all the quality checks are resolved
2. Run /gmb-test (use a subagent)
3. Run /source-commit and `sh commit.sh` on the resulting commit
4. Run /ncoderz-worklog
5. Run /gmb-pr in a subagent and capture the PR number/URL it creates
6. Run `/gh-pr-watch <pr-number-or-url>` passing that value as the `pr` argument
