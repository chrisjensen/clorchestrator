package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

// launchLabelSet opens one session per label: a plain labeled session for a
// single label, or — when there are 2+ labels — hive mode (a shared run dir,
// worker worktrees at <runDir>/<label>, plus a coordinator session). This is
// the one implementation shared by `open --benchmark` and batch's per-task
// `command:`/`--benchmark` handling, so both behave identically for the same
// label count. resolveBranch returns the branch to use for a given label
// (batch creates/reuses it via gh; open's own --benchmark just suffixes the
// user-supplied branch). baseRef, when non-empty, is the branch to check
// "commits ahead" against (batch's resolved default base branch) — passing
// "" skips that check, matching open's own --benchmark behavior.
func launchLabelSet(configPath, handle, baseBranch, baseRef string, cfg *config.Config, labels []string, baseOpts openOptions, resolveBranch func(label string) (string, error)) error {
	hive := len(labels) >= 2
	runDir := worktreePath(cfg.RemoteRepo, baseBranch, cfg.WorktreePrefix)

	for _, label := range labels {
		branch, err := resolveBranch(label)
		if err != nil {
			return err
		}

		worktreeDir := worktreePath(cfg.RemoteRepo, branch, cfg.WorktreePrefix)
		if hive {
			worktreeDir = filepath.Join(runDir, label)
		}

		opts := baseOpts
		opts.benchmarkLabel = label
		if baseRef != "" {
			ahead, err := branchAheadCount(cfg.Server, worktreeDir, baseRef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not check commits ahead for %s: %v — proceeding with planning session\n", branch, err)
				ahead = 0
			}
			if ahead > 0 {
				fmt.Fprintf(os.Stderr, "  Branch is %d commit(s) ahead of %s — opening shell in worktree (no Claude)\n", ahead, baseRef)
			}
			opts.noClaude = ahead > 0
		}
		if hive {
			opts.hiveRole = hiveRoleWorker
			opts.runDir = runDir
		}
		if err := openRun(configPath, handle, branch, opts); err != nil {
			return fmt.Errorf("open for %s/%s: %w", handle, label, err)
		}
	}

	if hive {
		// The coordinator has no issue/branch of its own — clear issue so
		// detectMode doesn't see a dangling issue with no branch.
		coordOpts := baseOpts
		coordOpts.issue = ""
		coordOpts.benchmarkLabel = ""
		coordOpts.hiveRole = hiveRoleCoordinator
		coordOpts.runDir = runDir
		coordOpts.commandLabel = labels[0]
		if err := openRun(configPath, handle, "", coordOpts); err != nil {
			return fmt.Errorf("open coordinator for %s: %w", handle, err)
		}
	}
	return nil
}

// worktreePath mirrors the layout used by scripts/worktree-checkout.sh:
// $(dirname REMOTE_REPO)/<prefix>-<sanitized-branch>. prefix defaults to the
// repo basename when empty. The server will expand ~ when the command runs.
func worktreePath(remoteRepo, branch, prefix string) string {
	repo := strings.TrimRight(remoteRepo, "/")
	parent := "."
	lastSlash := strings.LastIndex(repo, "/")
	if lastSlash > 0 {
		parent = repo[:lastSlash]
	} else if lastSlash == 0 {
		parent = "/"
	}
	if prefix == "" {
		prefix = repo[lastSlash+1:]
	}
	sanitized := strings.ReplaceAll(branch, "/", "-")
	return parent + "/" + prefix + "-" + sanitized
}
