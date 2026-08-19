package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/spf13/cobra"
)

func NewGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups [config]",
		Short: "list task groups and each sibling's completion status",
		Long: `List task groups (sets of sibling worktrees for the same task, one per
command label) and, for each configured label, whether its worktree is
done (has a .clorchestrate-done marker), running (worktree exists but
hasn't finished), or was never run for that task (omitted).

Without a config argument all configs in ~/.clorchestrate/ are scanned.
With a config argument only that config's worktrees are checked.

Use the resulting task keys with "review --evaluate --groups" to scope
an evaluation run to specific tasks.`,
		Args:              cobra.RangeArgs(0, 1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var configArg string
			if len(args) > 0 {
				configArg = args[0]
			}
			return groupsRun(configArg)
		},
	}
	return cmd
}

func groupsRun(configArg string) error {
	configPaths, err := resolveConfigPaths(configArg)
	if err != nil {
		return err
	}

	homeCache := map[string]string{}
	getHomeDir := func(server string) (string, error) {
		if h, ok := homeCache[server]; ok {
			return h, nil
		}
		h, err := remoteHomeDir(server)
		if err != nil {
			return "", err
		}
		homeCache[server] = h
		return h, nil
	}

	type siblingStatus struct {
		taskKey  string
		label    string
		status   string
		worktree string
	}
	var rows []siblingStatus

	for _, cfgPath := range configPaths {
		cfg, err := config.Parse(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skip %s: %v\n", cfgPath, err)
			continue
		}
		if len(cfg.Commands) == 0 {
			continue
		}

		scanCfgs := []*config.Config{cfg}
		for _, pkg := range cfg.Packages {
			pkgCfg, err := cfg.ResolvePackage(pkg.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: resolve package %s: %v\n", pkg.Name, err)
				continue
			}
			scanCfgs = append(scanCfgs, pkgCfg)
		}

		seen := map[string]bool{}
		for _, scanCfg := range scanCfgs {
			homeDir, err := getHomeDir(scanCfg.Server)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: get home dir: %v\n", err)
				continue
			}

			repo := strings.TrimRight(scanCfg.RemoteRepo, "/")
			repo = expandHome(repo, homeDir)
			parent := filepath.Dir(repo)
			prefix := scanCfg.WorktreePrefix
			if prefix == "" {
				prefix = filepath.Base(repo)
			}

			for _, command := range scanCfg.Commands {
				pattern := filepath.Join(parent, prefix+"-*-"+command.Label)
				matches, err := remoteGlob(scanCfg.Server, pattern)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: glob %s: %v\n", pattern, err)
					continue
				}

				for _, worktreeDir := range matches {
					if seen[worktreeDir] {
						continue
					}
					seen[worktreeDir] = true

					worktree := filepath.Base(worktreeDir)
					status := "running"
					if remoteFileExists(scanCfg.Server, filepath.Join(worktreeDir, ".clorchestrate-done")) {
						status = "done"
					}
					rows = append(rows, siblingStatus{
						taskKey:  taskKeyOf(worktree, command.Label),
						label:    command.Label,
						status:   status,
						worktree: worktree,
					})
				}
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].taskKey != rows[j].taskKey {
			return rows[i].taskKey < rows[j].taskKey
		}
		return rows[i].label < rows[j].label
	})

	fmt.Println("task_key\tlabel\tstatus\tworktree")
	for _, r := range rows {
		fmt.Printf("%s\t%s\t%s\t%s\n", r.taskKey, r.label, r.status, r.worktree)
	}
	return nil
}
