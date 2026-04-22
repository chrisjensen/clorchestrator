# Dispatch

A workflow automation system for spinning up parallel Claude coding sessions from a daily task list. Each task gets its own iTerm2 tab, screen session, git worktree, and a Claude instance pre-loaded with issue context and a planning prompt. When implementation is done, a composable skill sequence handles quality checks, review, commit, and PR creation.

---

## How it works

```
~/today.md
    │
    ▼
clorchestrate.sh <config> <tasks.md>    (runs on local Mac)
    │
    ├─ reads each ## <handle> block
    ├─ creates a GitHub branch linked to the issue (gh issue develop)
    └─ calls tab.sh per entry, which:
           ├─ writes /tmp/task-<handle>.conf to the server
           └─ opens an iTerm2 tab:
                  ssh → screen -S <handle> → start-task.sh <handle>
                                           │
                                           ├─ fetches issue title + body from GitHub
                                           ├─ worktree-checkout.sh (creates worktree)
                                           └─ launches claude with planning prompt
                                                  │
                                                  └─ on completion: /gmb-finish
                                                         ├─ /quality-correctness
                                                         ├─ /quality-checks
                                                         ├─ /quality-consistency
                                                         ├─ /quality-dry
                                                         ├─ /quality-review
                                                         ├─ /gmb-test
                                                         ├─ /source-commit
                                                         ├─ /ncoderz-worklog
                                                         ├─ /gmb-pr
                                                         └─ /gh-pr-watch
```

Screen sessions survive network interruptions — if SSH drops, `screen -r <handle>` reattaches.

---

## Task file format

Any `.md` file with `## <handle>` sections:

```markdown
## blueprint-parser

getMoreBrain/cosmic#456

Focus on the parser layer. Extend BlueprintParser rather than
modifying AiOcrToBitmarkJson directly.

## hotfix-output

https://github.com/getMoreBrain/cosmic/issues/458
base: release/1.2

Minimal fix only — do not refactor surrounding code.
```

- `## <handle>` — the task name; used for the screen session, worktree subdirectory, and temp config filename
- Issue reference — extracted by regex from anywhere in the block. Accepts `#NNN`, `org/repo#NNN`, or a full `https://github.com/org/repo/issues/NNN` URL
- `base: <branch>` — optional; overrides the default base branch for this task only
- Everything else — passed as extra context to the planning prompt

Run any `.md` file through clorchestrate:

```bash
clorchestrate.sh ~/.dispatch/bitmark-extractor-ai.conf today.md
clorchestrate.sh ~/.dispatch/bitmark-extractor-ai.conf sprint-extras.md
```

---

## Config file

One config per repo, stored at `~/.dispatch/<repo-name>.conf` on your local Mac.

```bash
DISPATCH_SERVER=myserver
DISPATCH_REMOTE_REPO=~/src/ncoderz/bitmark-extractor-ai
DISPATCH_ISSUE_REPO=getMoreBrain/cosmic
DISPATCH_BRANCH_REPO=getMoreBrain/bitmark-extractor-ai
DISPATCH_DEFAULT_BASE=main

# Branch naming:
# Empty = GitHub auto-generates name (matches "Create branch" UI behaviour)
# Set tokens {issue} and/or {handle} to enforce a format via --name
DISPATCH_BRANCH_NAME_FORMAT=""

# Runs in the new worktree after node_modules copy. Empty = skip.
DISPATCH_POST_SETUP_CMD="npm run build"

# Injected into every planning prompt for this repo.
DISPATCH_PLANNING_CONTEXT="..."
```

To use with a different repo, create a separate `.conf` and pass it as the first argument.

---

## Files

```
dispatch/
├── DISPATCH.md                        this file
├── install.sh                         installs server-side components
│
├── local/
│   ├── clorchestrate.sh               → ~/bin/clorchestrate.sh on local Mac  (iterator)
│   └── tab.sh                         → ~/bin/tab.sh on local Mac            (per-session launcher)
│
├── server/
│   ├── start-task.sh                  → ~/bin/start-task.sh on server
│   └── worktree-checkout.sh           → ~/bin/worktree-checkout.sh on server
│
├── config/
│   └── bitmark-extractor-ai.conf.example   → ~/.dispatch/bitmark-extractor-ai.conf on Mac
│
└── skills/
    ├── repo/                          → <repo>/.claude/skills/<name>/SKILL.md
    │   ├── gmb-finish/SKILL.md        orchestrates the finish sequence for this repo
    │   ├── gmb-pr/SKILL.md            pushes branch and creates PR
    │   └── gmb-test/SKILL.md          repo-specific integration/smoke test step
    │
    └── global/                        → ~/.claude/skills/<name>/SKILL.md on server
        ├── gh-pr-watch/SKILL.md       waits for PR checks to pass
        ├── ncoderz-worklog/SKILL.md   writes worklog + commit.sh
        ├── quality-checks/SKILL.md    runs linter, prettier, git hooks
        ├── quality-consistency/SKILL.md   checks deviation from architecture
        ├── quality-correctness/SKILL.md   runs typecheck, tests
        ├── quality-dry/SKILL.md       DRY review
        ├── quality-review/SKILL.md    clarity/conciseness review
        └── source-commit/SKILL.md     writes and executes commit.sh
```

### `clorchestrate.sh` (local Mac)

Reads the task file and, per entry, creates a GitHub branch linked to the issue tracker repo (`gh issue develop --branch-repo`). Then delegates to `tab.sh` to open one iTerm2 tab per task.

### `tab.sh` (local Mac)

Per-session launcher. Three call shapes:

- `tab.sh <config>` — opens an iTerm2 tab that SSHes to `$DISPATCH_SERVER` and lands in `$DISPATCH_REMOTE_REPO`. No worktree, no screen, no Claude.
- `tab.sh <config> <handle> <branch>` — writes the task config to the server, opens an iTerm2 tab that runs `start-task.sh` in a named screen session. `claude` launches with no prompt.
- `tab.sh <config> <handle> <branch> --issue <N|url> [--extra-context <text>]` — same, but with a planning prompt. `--issue` accepts a number, `#NNN`, `org/repo#NNN`, or a full GitHub issue URL.

This is what `clorchestrate.sh` calls for each task, and can also be run standalone.

### `start-task.sh` (server)

Sources the task config written by `tab.sh`, fetches the issue title and body from GitHub, runs `worktree-checkout.sh`, then launches `claude`. When `ISSUE` is set in the config, the planning prompt includes repo context, issue details, and any extra context from the task file; otherwise `claude` launches with no prompt.

### `worktree-checkout.sh` (server)

Checks out an existing remote branch into a new git worktree. Copies `.claude/` (skills + settings, which target repos gitignore) and `.env`, symlinks `resources/config` and `resources/output`, copies `node_modules` synchronously, then runs the configured post-setup command (e.g. `npm run build`).

This is the counterpart to the repo's `worktree.sh` — use `worktree.sh` when creating a new branch manually; use `worktree-checkout.sh` when the branch already exists on GitHub (as created by `clorchestrate.sh`).

### Skills

Each skill is a directory containing `SKILL.md`. Claude Code discovers skills under `~/.claude/skills/<name>/` (personal) and `<repo>/.claude/skills/<name>/` (project). Skills are invoked as `/<name>` — there is no `:` namespace for personal or project skills (that syntax only works for plugin skills).

**Global skills** (installed by `install.sh` into `~/.claude/skills/`):
- `/quality-correctness` — typecheck, tests, correctness review
- `/quality-checks` — linter, prettier, git hooks
- `/quality-consistency` — architectural/convention drift
- `/quality-dry` — DRY review
- `/quality-review` — clarity and conciseness review
- `/source-commit` — writes `commit.sh` matching repo style, then executes it
- `/ncoderz-worklog` — appends to the project worklog
- `/gh-pr-watch <pr>` — background-waits for PR checks to pass

**Repo skills** (installed into `<repo>/.claude/skills/` when `REPO_ROOT` is passed to `install.sh`):
- `/gmb-test` — runs the integration/smoke tests for the target repo
- `/gmb-pr` — pushes the branch and creates the PR with `gh pr create`
- `/gmb-finish` — orchestrates the finish sequence. It is a plain skill whose body lists `/quality-*`, `/gmb-test`, `/source-commit`, `/ncoderz-worklog`, `/gmb-pr`, `/gh-pr-watch`. Claude reads the list and invokes each in turn; sequencing is best-effort, not a runtime contract. Repos customise by editing `gmb-finish/SKILL.md` (or forking it under a different name).

---

## Installation

### On the server

```bash
cd dispatch && ./install.sh
```

This copies server scripts to `~/bin/` and global skills to `~/.claude/skills/workflow/`.

For repo-specific skills, `install.sh` will copy them to `.claude/skills/workflow/` if `.claude/skills` is a directory. If it shows a warning, follow the manual steps it prints.

### On your local Mac

```bash
mkdir -p ~/bin
cp dispatch/local/clorchestrate.sh ~/bin/clorchestrate.sh
cp dispatch/local/tab.sh ~/bin/tab.sh
chmod +x ~/bin/clorchestrate.sh ~/bin/tab.sh

mkdir -p ~/.dispatch
cp dispatch/config/bitmark-extractor-ai.conf.example ~/.dispatch/bitmark-extractor-ai.conf
# Edit ~/.dispatch/bitmark-extractor-ai.conf with real values
```

### Before first run

Verify how `gh issue develop` formats its output on this machine — the branch name is parsed from stdout:

```bash
gh issue develop --list 1 --repo getMoreBrain/cosmic
```

If the output format differs from `github.com/org/repo/tree/branch-name`, update the parsing line in `clorchestrate.sh`.
