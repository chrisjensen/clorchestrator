---
description: Write a commit.sh file to commit the changes in this branch
model: sonnet
effort: low
---

Push the branch to origin, then create a pull request with `gh pr create`.

- Include a `Closes {issue}` in the body
- The convention is that issues are stored in the cosmic repo, so the format will be `Closes GetMoreBrain/cosmic#{issueNum}`
- Keep the title concise (under 70 characters)

Keep the body concise, limit it to descriptions of the key changes

