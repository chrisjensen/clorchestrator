package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/prompts"
	"github.com/spf13/cobra"
)

func NewReviewCmd() *cobra.Command {
	var evaluate, fresh bool
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
are deleted before starting, forcing a full re-evaluation.`,
		Args:              cobra.RangeArgs(0, 1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var configArg string
			if len(args) > 0 {
				configArg = args[0]
			}
			return reviewRun(configArg, evaluate, fresh)
		},
	}
	cmd.Flags().BoolVar(&evaluate, "evaluate", false, "launch evaluation agents per worktree and add quality columns to TSV")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "delete cached evaluation results and re-run (implies --evaluate)")
	return cmd
}

type tokenTotals struct {
	label               string
	worktree            string
	sessions            int
	inputTokens         int
	outputTokens        int
	cacheReadTokens     int
	cacheCreationTokens int
	// populated when --evaluate is set
	dir       string
	server    string
	homeDir   string
	parent    string
	claudeCmd string
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

func reviewRun(configArg string, evaluate, fresh bool) error {
	if fresh {
		evaluate = true
	}
	var configPaths []string
	if configArg != "" {
		p, err := resolveConfigPath(configArg)
		if err != nil {
			return err
		}
		configPaths = []string{p}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(home, ".clorchestrate"))
		if err != nil {
			return fmt.Errorf("read ~/.clorchestrate: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				configPaths = append(configPaths, filepath.Join(home, ".clorchestrate", e.Name()))
			}
		}
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

			for _, command := range scanCfg.Commands {
				// Glob for worktrees ending with the label suffix.
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
					// Only evaluate worktrees where the session ran to completion.
					if !remoteFileExists(scanCfg.Server, filepath.Join(worktreeDir, ".clorchestrate-done")) {
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
					if evaluate {
						totals.dir = worktreeDir
						totals.server = scanCfg.Server
						totals.homeDir = homeDir
						totals.parent = parent
						totals.claudeCmd = command.Cmd
					}
					rows = append(rows, totals)
				}
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

// runGroupedEvaluations groups rows by task key (worktree base name with label suffix stripped),
// then launches an evaluation agent for each directory in a group, informing it of the others.
// Evaluation results are written back into the rows slice in place.
func runGroupedEvaluations(rows []tokenTotals) {
	groups := map[string][]int{}
	for i, r := range rows {
		taskKey := strings.TrimSuffix(r.worktree, "-"+r.label)
		groups[taskKey] = append(groups[taskKey], i)
	}

	for taskKey, indices := range groups {
		allDone := true
		for _, i := range indices {
			r := rows[i]
			doneFile := filepath.Join(r.dir, ".clorchestrate-done")
			if !remoteFileExists(r.server, doneFile) {
				fmt.Fprintf(os.Stderr, "skipping group %s: %s missing .clorchestrate-done\n", taskKey, r.worktree)
				allDone = false
				break
			}
		}
		if !allDone {
			continue
		}

		groupRows := make([]tokenTotals, len(indices))
		for j, i := range indices {
			groupRows[j] = rows[i]
		}

		for j, i := range indices {
			target := rows[i]
			others := make([]tokenTotals, 0, len(groupRows)-1)
			for k, r := range groupRows {
				if k != j {
					others = append(others, r)
				}
			}

			evalOthers := make([]prompts.EvalOther, len(others))
			for i, o := range others {
				evalOthers[i] = prompts.EvalOther{
					Dir:         o.dir,
					Label:       o.label,
					ProjectsDir: claudeProjectsDir(o.homeDir, o.dir),
				}
			}
			prompt, err := prompts.Eval(prompts.EvalData{
				TargetDir:         target.dir,
				TargetLabel:       target.label,
				TargetProjectsDir: claudeProjectsDir(target.homeDir, target.dir),
				Others:            evalOthers,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: build eval prompt for %s: %v\n", target.worktree, err)
				continue
			}
			if err := writeEvalPrompt(target.server, target.worktree, prompt); err != nil {
				fmt.Fprintf(os.Stderr, "warning: write eval prompt for %s: %v\n", target.worktree, err)
				continue
			}

			rawOutput, err := runEvaluation(target.server, target.parent, target.worktree, target.claudeCmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: evaluate %s: %v\n", target.worktree, err)
				continue
			}

			jsonStr, prose, err := extractJSONAndProse(rawOutput)
			if err != nil {
				preview := strings.TrimSpace(rawOutput)
				if len(preview) > 300 {
					preview = preview[:300] + "..."
				}
				fmt.Fprintf(os.Stderr, "warning: parse evaluation output for %s: %v\n--- output ---\n%s\n---\n", target.worktree, err, preview)
				continue
			}

			var ev evalJSON
			if err := json.Unmarshal([]byte(jsonStr), &ev); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unmarshal evaluation JSON for %s: %v\n", target.worktree, err)
				continue
			}

			rows[i].eval = ev
			rows[i].prose = prose
		}

		computeCompleteness(rows, indices)
	}
}

func writeEvalPrompt(server, worktreeBase, prompt string) error {
	path := fmt.Sprintf("/tmp/clorchestrate-eval-%s.md", worktreeBase)
	if server == "" {
		return os.WriteFile(path, []byte(prompt), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write eval prompt on %s: %w", server, err)
	}
	return nil
}

func evalResultFile(worktreeBase string) string {
	return fmt.Sprintf("/tmp/clorchestrate-eval-%s.out", worktreeBase)
}

func evalScreenName(worktreeBase string) string {
	return "clorchestrate-eval-" + worktreeBase
}

// remoteFileExists returns true if the file exists (may be empty).
func remoteFileExists(server, path string) bool {
	if server == "" {
		_, err := os.Stat(path)
		return err == nil
	}
	return exec.Command("ssh", server, fmt.Sprintf("test -f %s", path)).Run() == nil
}

// remoteFileNonEmpty returns true if the file exists and has non-zero size.
func remoteFileNonEmpty(server, path string) bool {
	if server == "" {
		info, err := os.Stat(path)
		return err == nil && info.Size() > 0
	}
	return exec.Command("ssh", server, fmt.Sprintf("test -s %s", path)).Run() == nil
}

// screenSessionRunning returns true if a screen session with the given name exists on the server.
func screenSessionRunning(server, name string) bool {
	if server == "" {
		return false
	}
	return exec.Command("ssh", server, fmt.Sprintf("screen -ls | grep -qF '.%s'", name)).Run() == nil
}

// readRemoteFile reads a file from the server (or locally when server is "").
func readRemoteFile(server, path string) (string, error) {
	var out []byte
	var err error
	if server == "" {
		out, err = os.ReadFile(path)
	} else {
		out, err = exec.Command("ssh", server, fmt.Sprintf("cat %s", path)).Output()
	}
	if err != nil {
		return "", fmt.Errorf("read result file %s: %w", path, err)
	}
	return string(out), nil
}

// deleteEvalArtifacts removes the result file and kills any running screen session for a worktree.
func deleteEvalArtifacts(server, worktreeBase string) {
	resultFile := evalResultFile(worktreeBase)
	screenName := evalScreenName(worktreeBase)
	if server == "" {
		os.Remove(resultFile)
		return
	}
	shellCmd := fmt.Sprintf("rm -f %s; screen -S %s -X quit 2>/dev/null; true", resultFile, screenName)
	exec.Command("ssh", server, shellCmd).Run() //nolint:errcheck
}

// isRetryableAPIError returns true when the output is an API-level error that is
// worth retrying (e.g. 529 service overloaded). Other errors (auth, bad request)
// are not retried since they will not resolve on their own.
func isRetryableAPIError(output string) bool {
	return strings.Contains(output, "API Error: 529")
}

// runEvaluation launches the evaluation for a single worktree, retrying up to 3
// times if the result file contains a retryable API error (e.g. 529 overloaded).
func runEvaluation(server, parent, worktreeBase, claudeCmd string) (string, error) {
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := attemptEvaluation(server, parent, worktreeBase, claudeCmd)
		if err != nil {
			return "", err
		}
		if !isRetryableAPIError(result) {
			return result, nil
		}
		if attempt < maxAttempts {
			fmt.Fprintf(os.Stderr, "evaluation %s: 529 error (attempt %d/%d), retrying in 60s\n", worktreeBase, attempt, maxAttempts)
			deleteEvalArtifacts(server, worktreeBase)
			time.Sleep(60 * time.Second)
		}
	}
	return "", fmt.Errorf("evaluation %s: failed after %d attempts (529 service overloaded)", worktreeBase, maxAttempts)
}

// attemptEvaluation runs a single evaluation attempt using a detached screen session
// (remote) or backgrounded nohup process (local), writing output to a result file.
// On re-run it reuses a cached result file or reconnects to an in-progress session.
func attemptEvaluation(server, parent, worktreeBase, claudeCmd string) (string, error) {
	promptPath := fmt.Sprintf("/tmp/clorchestrate-eval-%s.md", worktreeBase)
	resultFile := evalResultFile(worktreeBase)
	screenName := evalScreenName(worktreeBase)

	// Cached: result file already written by a previous (possibly interrupted) run.
	if remoteFileNonEmpty(server, resultFile) {
		fmt.Fprintf(os.Stderr, "evaluation %s: using cached result\n", worktreeBase)
		return readRemoteFile(server, resultFile)
	}

	// In-progress: screen session is still running from a dropped connection.
	if screenSessionRunning(server, screenName) {
		fmt.Fprintf(os.Stderr, "evaluation %s: reconnecting to running session\n", worktreeBase)
	} else {
		// Launch a new evaluation.
		fmt.Fprintf(os.Stderr, "launching evaluation for %s\n", worktreeBase)
		var cmd *exec.Cmd
		if server == "" {
			// nohup backgrounds the process so it survives terminal close / suspend.
			shellCmd := fmt.Sprintf(
				"nohup sh -c 'cd %s && %s --print \"$(cat %s)\" > %s 2>&1' >/dev/null 2>&1 &",
				parent, claudeCmd, promptPath, resultFile,
			)
			cmd = exec.Command("sh", "-c", shellCmd)
		} else {
			// screen -dm starts detached; bash -lc sources the login profile for PATH.
			screenCmd := fmt.Sprintf(
				"screen -dmS %s bash -lc 'cd %s && %s --print \"$(cat %s)\" > %s 2>&1'",
				screenName, parent, claudeCmd, promptPath, resultFile,
			)
			cmd = exec.Command("ssh", server, screenCmd)
		}
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("launch evaluation for %s: %w", worktreeBase, err)
		}
	}

	// Poll until the result file appears (local) or the screen session exits (remote).
	fmt.Fprintf(os.Stderr, "waiting for evaluation %s...\n", worktreeBase)
	for {
		time.Sleep(10 * time.Second)

		if server != "" && screenSessionRunning(server, screenName) {
			continue
		}

		if remoteFileNonEmpty(server, resultFile) {
			fmt.Fprintf(os.Stderr, "evaluation %s: complete\n", worktreeBase)
			return readRemoteFile(server, resultFile)
		}

		if server == "" {
			// Local process still running — keep polling until the file appears.
			continue
		}

		// Remote: screen session exited but no result file — process likely failed.
		return "", fmt.Errorf("evaluation %s: screen session exited without producing output", worktreeBase)
	}
}

// findJSONBounds returns the start index and one-past-end index of the first
// complete JSON object in s, walking character by character to correctly match
// braces inside strings and ignore braces in surrounding prose.
func findJSONBounds(s string) (start, end int, ok bool) {
	start = strings.Index(s, "{")
	if start == -1 {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return 0, 0, false
}

// extractJSONAndProse finds the JSON object in output (stripping markdown code fences and
// surrounding prose) and returns it along with any trailing prose description.
// The output may contain prose before the JSON, a ```json fence around it, or both.
func extractJSONAndProse(output string) (string, string, error) {
	s := strings.TrimSpace(output)

	// If there's a code fence anywhere, extract the JSON from inside it.
	// Any text before the opening fence or after the closing fence becomes part of prose.
	if fenceIdx := strings.Index(s, "```"); fenceIdx != -1 {
		inner := s[fenceIdx+3:]
		// skip optional language tag line (e.g. "json\n")
		if nl := strings.Index(inner, "\n"); nl != -1 {
			inner = inner[nl+1:]
		}
		if closeIdx := strings.Index(inner, "```"); closeIdx != -1 {
			candidate := strings.TrimSpace(inner[:closeIdx])
			// Prose = text before the opening fence + text after the closing fence.
			beforeFence := strings.TrimSpace(s[:fenceIdx])
			afterFence := strings.TrimSpace(inner[closeIdx+3:])
			prose := strings.TrimSpace(beforeFence + "\n\n" + afterFence)
			if start, end, ok := findJSONBounds(candidate); ok {
				jsonStr := candidate[start:end]
				if json.Valid([]byte(jsonStr)) {
					return jsonStr, prose, nil
				}
			}
		}
	}

	// No fences (or fence extraction failed): find first complete JSON object by
	// depth-counting braces so prose containing { } doesn't confuse the parser.
	start, end, ok := findJSONBounds(s)
	if !ok {
		return "", "", fmt.Errorf("no JSON object found in evaluation output")
	}
	jsonStr := s[start:end]
	if !json.Valid([]byte(jsonStr)) {
		return "", "", fmt.Errorf("invalid JSON in evaluation output")
	}
	prose := strings.TrimSpace(s[end:])
	return jsonStr, prose, nil
}

// computeCompleteness sets each row's complete score independently using its own all_aspects
// and missing lists. Score = (all_aspects_count - missing_count) * 100 / all_aspects_count.
// Scores across rows in a group are not directly comparable (each LLM uses its own phrasing)
// but each score is internally consistent and meaningful.
func computeCompleteness(rows []tokenTotals, indices []int) {
	for _, i := range indices {
		allCount := len(splitAspects(rows[i].eval.AllAspects))
		if allCount == 0 {
			continue
		}
		missingCount := len(splitAspects(rows[i].eval.Missing))
		score := (allCount - missingCount) * 100 / allCount
		if score < 0 {
			score = 0
		}
		rows[i].complete = score
		rows[i].aspectCount = allCount
	}
}

func splitAspects(s string) []string {
	var result []string
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(strings.ToLower(a))
		if a != "" {
			result = append(result, a)
		}
	}
	return result
}

// remoteHomeDir returns the home directory on the given server (empty = local).
func remoteHomeDir(server string) (string, error) {
	var out []byte
	var err error
	if server == "" {
		home, e := os.UserHomeDir()
		return home, e
	}
	out, err = exec.Command("ssh", server, "echo $HOME").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// expandHome replaces a leading "~" with homeDir.
func expandHome(path, homeDir string) string {
	if strings.HasPrefix(path, "~/") {
		return homeDir + path[1:]
	}
	if path == "~" {
		return homeDir
	}
	return path
}

// remoteGlob runs a shell glob on the server and returns matching paths.
func remoteGlob(server, pattern string) ([]string, error) {
	shellCmd := fmt.Sprintf("ls -d %s 2>/dev/null || true", pattern)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, shellCmd).Output()
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// claudeProjectsDir returns the Claude Code projects directory path for
// a given worktree absolute path. Claude Code encodes the project path by
// replacing each "/" with "-".
func claudeProjectsDir(homeDir, worktreePath string) string {
	encoded := strings.ReplaceAll(worktreePath, "/", "-")
	return filepath.Join(homeDir, ".claude", "projects", encoded)
}

// usageLine is the subset of a Claude Code JSONL line we care about.
type usageLine struct {
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// readWorktreeTokens reads the most recent Claude Code JSONL transcript for a
// worktree and sums its token usage. Sessions counts all JSONL files present
// so the caller can see how many attempts existed.
func readWorktreeTokens(server, homeDir, worktreeDir string) (tokenTotals, error) {
	projectDir := claudeProjectsDir(homeDir, worktreeDir)
	// Sort by modification time (newest first) and read only the latest session.
	shellCmd := fmt.Sprintf("ls -t %s/*.jsonl 2>/dev/null | head -1 | xargs -r cat", projectDir)

	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, shellCmd).Output()
	}
	if err != nil {
		return tokenTotals{}, fmt.Errorf("read jsonl from %s: %w", projectDir, err)
	}

	// Count distinct JSONL files for the sessions field.
	sessionCount, err := countJSONLFiles(server, projectDir)
	if err != nil {
		sessionCount = 0
	}

	var totals tokenTotals
	totals.sessions = sessionCount

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record usageLine
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		u := record.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			continue
		}
		totals.inputTokens += u.InputTokens
		totals.outputTokens += u.OutputTokens
		totals.cacheReadTokens += u.CacheReadInputTokens
		totals.cacheCreationTokens += u.CacheCreationInputTokens
	}
	return totals, nil
}

// countJSONLFiles returns the number of .jsonl files in a directory.
func countJSONLFiles(server, dir string) (int, error) {
	shellCmd := fmt.Sprintf("ls %s/*.jsonl 2>/dev/null | wc -l", dir)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, shellCmd).Output()
	}
	if err != nil {
		return 0, err
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n, nil
}
