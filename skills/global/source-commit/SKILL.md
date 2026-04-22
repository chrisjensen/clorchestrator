---
description: Write a commit.sh file to commit the changes in this branch
model: sonnet
effort: medium
---

Write a commit.sh file that calls git to add and group the changes that have been made
Logically group associated changes into the same commit
Use a shape up, vertical slices approach - logically group changes based on feature, not front-end/back-end

Avoid boasting or overstating the work done in the commit messages. (eg the commit doesn't contain "comprehensive tests" it "improves test coverage")

Prefer brevity in commit messages. Each message should be easily skimmable

Avoid co-authors
Avoid co-authors

If commit.sh already exists, check `git status` to ensure the commit script matches the current state of the repository.

No need to chmod +x the file, run it with `sh commit.sh`

