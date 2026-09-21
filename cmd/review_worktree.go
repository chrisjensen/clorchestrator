package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

// taskKeyOf returns the task key for a worktree given its command label —
// the worktree base name with its trailing "-<label>" suffix stripped.
// Sibling worktrees for the same task (different command labels) share a
// task key.
func taskKeyOf(worktree, label string) string {
	return strings.TrimSuffix(worktree, "-"+label)
}

// discoveredWorktree is one benchmark worktree found on disk for a command
// label, in either the legacy sibling layout (<prefix>-<base>-<label>) or the
// hive child layout (<prefix>-<base>/<label>, with sentinels at the run root).
type discoveredWorktree struct {
	dir     string // worktree path
	taskKey string // shared across the labels of one task
	runDir  string // hive run dir (parent of dir); "" in the sibling layout
}

// discoverWorktrees globs both benchmark layouts for a command label. parent is
// the repo's parent directory, prefix the worktree prefix.
func discoverWorktrees(server, parent, prefix, label string) ([]discoveredWorktree, error) {
	var out []discoveredWorktree
	// Legacy sibling layout: <parent>/<prefix>-<base>-<label>.
	siblings, err := remoteGlob(server, filepath.Join(parent, prefix+"-*-"+label))
	if err != nil {
		return nil, err
	}
	for _, dir := range siblings {
		out = append(out, discoveredWorktree{dir: dir, taskKey: taskKeyOf(filepath.Base(dir), label)})
	}
	// Hive child layout: <parent>/<prefix>-<base>/<label>. The task key is the
	// run dir's base name (workers of one task share it).
	children, err := remoteGlob(server, filepath.Join(parent, prefix+"-*", label))
	if err != nil {
		return nil, err
	}
	for _, dir := range children {
		runDir := filepath.Dir(dir)
		out = append(out, discoveredWorktree{dir: dir, taskKey: filepath.Base(runDir), runDir: runDir})
	}
	return out, nil
}

// worktreeDone reports whether a worktree's session ran to completion: either
// the legacy <worktree>/.clorchestrate-done marker, or the hive sentinel
// <runDir>/<label>.impl.done at the run-dir root.
func worktreeDone(server string, w discoveredWorktree, label string) bool {
	if remoteFileExists(server, filepath.Join(w.dir, ".clorchestrate-done")) {
		return true
	}
	if w.runDir != "" && remoteFileExists(server, filepath.Join(w.runDir, label+".impl.done")) {
		return true
	}
	return false
}

// resolveConfigPaths returns the config file(s) to scan: just configArg if
// given (resolved via resolveConfigPath), otherwise every *.toml file in
// ~/.clorchestrate/.
func resolveConfigPaths(configArg string) ([]string, error) {
	if configArg != "" {
		p, err := resolveConfigPath(configArg)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(home, ".clorchestrate"))
	if err != nil {
		return nil, fmt.Errorf("read ~/.clorchestrate: %w", err)
	}
	var configPaths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			configPaths = append(configPaths, filepath.Join(home, ".clorchestrate", e.Name()))
		}
	}
	return configPaths, nil
}

// markIncompleteGroups checks, for each distinct task key among rows, whether
// every other command label configured for scanCfg that has an existing
// worktree for that task has finished (.clorchestrate-done present). A
// sibling label only counts if its worktree directory exists on disk, so
// --benchmark runs that only used a subset of the configured labels aren't
// penalized for labels that were never run.
//
// If a task has an existing-but-unfinished sibling and force is false, every
// row sharing that task key is marked skipEval so runGroupedEvaluations will
// leave it out of the comparison (it still appears in the plain TSV output).
// If force is true, the task proceeds using whichever siblings are done.
func markIncompleteGroups(rows []tokenTotals, scanCfg *config.Config, force bool) {
	checked := map[string]bool{}
	for i := range rows {
		taskKey := rows[i].taskKey
		if checked[taskKey] {
			continue
		}
		checked[taskKey] = true

		var missing []string
		for _, cmd := range scanCfg.Commands {
			if cmd.Label == rows[i].label {
				continue
			}
			// The sibling shares this row's layout: hive siblings live under the
			// run dir, legacy siblings alongside the repo.
			var sib discoveredWorktree
			if rows[i].runDir != "" {
				sib = discoveredWorktree{dir: filepath.Join(rows[i].runDir, cmd.Label), runDir: rows[i].runDir}
			} else {
				sib = discoveredWorktree{dir: filepath.Join(rows[i].parent, taskKey+"-"+cmd.Label)}
			}
			if !remoteDirExists(rows[i].server, sib.dir) {
				continue // that label was never run for this task
			}
			if !worktreeDone(rows[i].server, sib, cmd.Label) {
				missing = append(missing, cmd.Label)
			}
		}
		if len(missing) == 0 {
			continue
		}

		if force {
			fmt.Fprintf(os.Stderr, "group %s: evaluating despite incomplete siblings %v (--force)\n", taskKey, missing)
			continue
		}

		fmt.Fprintf(os.Stderr, "skipping group %s: siblings not done yet: %v\n", taskKey, missing)
		for j := range rows {
			if rows[j].taskKey == taskKey {
				rows[j].skipEval = true
			}
		}
	}
}
