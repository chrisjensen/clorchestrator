package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/branchdetect"
	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/spf13/cobra"
)

func NewReviewCmd() *cobra.Command {
	var evaluate, fresh, force bool
	var groups []string
	cmd := &cobra.Command{
		Use:   "review [config]",
		Short: "evaluate and compare benchmark implementations across command variants",
		Long: `Compare benchmark worktrees across command variants and output results as TSV.

Without a config argument all configs in ~/.clorchestrate/ are scanned.
With a config argument only that config's worktrees are checked.

Benchmark worktrees are directories whose name ends with a command label
defined in the config's [[command]] blocks.

With --evaluate, a non-interactive Claude session is launched in a detached
screen session on the server for each worktree to assess correctness,
completeness, clarity, and complexity across implementations. Each evaluation
sees the other variants' worktrees for comparison. Output is written to a
result file so that reconnecting after a dropped connection resumes rather
than restarts. Evaluation columns are appended to the TSV output.

With --fresh, cached result files and any running evaluation screen sessions
are deleted before starting, forcing a full re-evaluation.

With --groups, only worktrees belonging to the given task groups (comma-
separated task keys — a worktree's name with its trailing -<label> suffix
stripped) are included, in both plain and --evaluate output. Use the
"groups" command to list available task keys.

A task's variants are only evaluated once every benchmark label configured
for its worktree has finished (has a .clorchestrate-done marker in an
existing sibling worktree) — otherwise the group is skipped with a warning
so comparisons aren't made against still-running siblings. With --force,
evaluation proceeds using whichever variants are done, even if others are
still running.`,
		Args:              cobra.RangeArgs(0, 1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var configArg string
			if len(args) > 0 {
				configArg = args[0]
			}
			return reviewRun(configArg, evaluate, fresh, force, groups)
		},
	}
	cmd.Flags().BoolVar(&evaluate, "evaluate", false, "launch evaluation agents per worktree and add quality columns to TSV")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "delete cached evaluation results and re-run (implies --evaluate)")
	cmd.Flags().BoolVar(&force, "force", false, "evaluate a task's variants even if some haven't finished (missing .clorchestrate-done)")
	cmd.Flags().StringSliceVar(&groups, "groups", nil, "only include these task groups (comma-separated task keys — see the \"groups\" command)")
	return cmd
}

type tokenTotals struct {
	label               string
	worktree            string
	taskKey             string // task-group identity, layout-agnostic
	runDir              string // hive run dir; "" in the legacy sibling layout
	sessions            int
	inputTokens         int
	outputTokens        int
	cacheReadTokens     int
	cacheCreationTokens int
	// populated when --evaluate is set
	dir         string
	server      string
	homeDir     string
	parent      string
	claudeCmd   string
	baseBranch  string
	skipEval    bool // set when this row's task-group has an incomplete sibling and --force wasn't passed
	eval        evalJSON
	complete    int
	aspectCount int // count of aspects in this row's all_aspects list
	prose       string
}

type evalJSON struct {
	Problem     string `json:"problem"`
	Clarity     string `json:"clarity"`
	Complexity  string `json:"complexity"`
	Correct     *bool  `json:"correct"`
	AllAspects  string `json:"all_aspects"`
	Implemented string `json:"implemented"`
	Missing     string `json:"missing"`
}

func reviewRun(configArg string, evaluate, fresh, force bool, groups []string) error {
	if fresh {
		evaluate = true
	}
	configPaths, err := resolveConfigPaths(configArg)
	if err != nil {
		return err
	}

	// homeCache avoids repeated SSH round-trips for the same server.
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

	resolver := branchdetect.NewResolver()

	var rows []tokenTotals
	for _, cfgPath := range configPaths {
		cfg, err := config.Parse(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skip %s: %v\n", cfgPath, err)
			continue
		}
		if len(cfg.Commands) == 0 {
			continue
		}

		cfgRowStart := len(rows)
		// seen deduplicates worktree dirs that appear under multiple package prefixes.
		seen := map[string]bool{}

		// Scan base config then each package (packages may have a different
		// worktree_prefix or remote_repo, producing differently-named worktree dirs).
		scanCfgs := []*config.Config{cfg}
		for _, pkg := range cfg.Packages {
			pkgCfg, err := cfg.ResolvePackage(pkg.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: resolve package %s: %v\n", pkg.Name, err)
				continue
			}
			scanCfgs = append(scanCfgs, pkgCfg)
		}

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

			scanCfgStart := len(rows)
			for _, command := range scanCfg.Commands {
				// Glob both the legacy sibling and hive child layouts.
				worktrees, err := discoverWorktrees(scanCfg.Server, parent, prefix, command.Label)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: glob for %s: %v\n", command.Label, err)
					continue
				}

				for _, w := range worktrees {
					worktreeDir := w.dir
					if seen[worktreeDir] {
						continue
					}
					// Only evaluate worktrees where the session ran to completion.
					if !worktreeDone(scanCfg.Server, w, command.Label) {
						continue
					}
					if len(groups) > 0 && !slices.Contains(groups, w.taskKey) {
						continue
					}
					seen[worktreeDir] = true

					totals, err := readWorktreeTokens(scanCfg.Server, homeDir, worktreeDir)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: read tokens for %s: %v\n", worktreeDir, err)
						continue
					}
					totals.label = command.Label
					totals.worktree = filepath.Base(worktreeDir)
					totals.taskKey = w.taskKey
					totals.runDir = w.runDir
					if evaluate {
						totals.dir = worktreeDir
						totals.server = scanCfg.Server
						totals.homeDir = homeDir
						totals.parent = parent
						totals.claudeCmd = command.Cmd
						totals.baseBranch = scanCfg.DefaultBase
						if totals.baseBranch == "" {
							resolved, err := resolver.Resolve(scanCfg.Server, scanCfg.RemoteRepo, scanCfg.BranchRepo, scanCfg.IssueRepo)
							if err != nil {
								fmt.Fprintf(os.Stderr, "warning: resolve default branch for %s: %v\n", worktreeDir, err)
							} else {
								totals.baseBranch = resolved
							}
						}
						if command.EvaluateWith != "" {
							evalCmd, err := scanCfg.ResolveCommand(command.EvaluateWith)
							if err != nil {
								fmt.Fprintf(os.Stderr, "warning: evaluate_with %q for command %q: %v\n", command.EvaluateWith, command.Label, err)
							} else {
								totals.claudeCmd = evalCmd.Cmd
							}
						}
					}
					rows = append(rows, totals)
				}
			}

			if evaluate {
				markIncompleteGroups(rows[scanCfgStart:], scanCfg, force)
			}
		}

		if evaluate && len(rows) > cfgRowStart {
			if fresh {
				for i := cfgRowStart; i < len(rows); i++ {
					deleteEvalArtifacts(rows[i].server, rows[i].worktree)
				}
			}
			// cfgRows is a slice of the same backing array — mutations propagate to rows.
			cfgRows := rows[cfgRowStart:]
			runGroupedEvaluations(cfgRows)
		}
	}

	// Print TSV header then rows.
	if evaluate {
		fmt.Println("label\tworktree\tsessions\tinput_tokens\toutput_tokens\tcache_read_tokens\tcache_creation_tokens\tproblem\tclarity\tcomplexity\tcorrect\tcomplete\tall_aspects\tall_aspects_count\tmissing\tmissing_count")
		for _, r := range rows {
			correctStr := ""
			if r.eval.Correct != nil {
				correctStr = fmt.Sprintf("%v", *r.eval.Correct)
			}
			fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\t%d\n",
				r.label, r.worktree, r.sessions,
				r.inputTokens, r.outputTokens, r.cacheReadTokens, r.cacheCreationTokens,
				r.eval.Problem, r.eval.Clarity, r.eval.Complexity,
				correctStr, r.complete,
				r.eval.AllAspects, r.aspectCount,
				r.eval.Missing, len(splitAspects(r.eval.Missing)))
		}
	} else {
		fmt.Println("label\tworktree\tsessions\tinput_tokens\toutput_tokens\tcache_read_tokens\tcache_creation_tokens")
		for _, r := range rows {
			fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
				r.label, r.worktree, r.sessions,
				r.inputTokens, r.outputTokens, r.cacheReadTokens, r.cacheCreationTokens)
		}
	}
	return nil
}
