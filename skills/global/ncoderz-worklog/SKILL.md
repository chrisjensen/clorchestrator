---
description: Write a commit.sh file to commit the changes in this branch
model: sonnet
effort: medium
---

Update ~/src/ncoderz/worklog/WORKLOG.md with the work that's been done

Match the style and brevity of other entries in the worklog

Only include significant work.

As a general rule a typical branch will result in 2-4 entries:
- Code/Fix: Write the code / fix
- Debug: Fix or rework a major blocker
- Test: Write a new test suite for the new feature
- Test: Long running tests done beyong the CI/CD tests to guarantee change is working

Only significant work should be listed, examples of things that are significant
- Debug a significant blocker
- Add a new test suite
- Add a new module or refactor existing code so as to make it more DRY

Examples of things that are not sufficiently significant to include
- Updating an existing test suite or adding a single test
- Fixing linting/typing errors