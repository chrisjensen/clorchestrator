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

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/spf13/cobra"
)

func NewReviewCmd() *cobra.Command {
	var evaluate bool
	cmd := &cobra.Command{
		Use:   "review [config]",
		Short: "review token usage across benchmark sessions",
		Long: `Scan benchmark worktrees and report token usage per label as TSV.

Without a config argument all configs in ~/.clorchestrate/ are scanned.
With a config argument only that config's worktrees are checked.

Benchmark worktrees are directories whose name ends with a command label
defined in the config's [[command]] blocks. Token data is read from the
Claude Code JSONL transcript files stored under ~/.claude/projects/ on the
machine where the sessions ran.

With --evaluate, a non-interactive Claude session is launched in the parent
directory for each worktree to assess correctness and completeness across
implementations. Evaluation columns are appended to the TSV output.`,
		Args:              cobra.RangeArgs(0, 1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var configArg string
			if len(args) > 0 {
				configArg = args[0]
			}
			return reviewRun(configArg, evaluate)
		},
	}
	cmd.Flags().BoolVar(&evaluate, "evaluate", false, "launch evaluation agents per worktree and add quality columns to TSV")
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
	eval      evalJSON
	complete  int
	prose     string
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

func reviewRun(configArg string, evaluate bool) error {
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

		homeDir, err := remoteHomeDir(cfg.Server)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skip %s: get home dir: %v\n", cfgPath, err)
			continue
		}

		repo := strings.TrimRight(cfg.RemoteRepo, "/")
		repo = expandHome(repo, homeDir)

		parent := filepath.Dir(repo)
		prefix := cfg.WorktreePrefix
		if prefix == "" {
			prefix = filepath.Base(repo)
		}

		cfgRowStart := len(rows)

		for _, command := range cfg.Commands {
			// Glob for worktrees ending with the label suffix.
			pattern := filepath.Join(parent, prefix+"-*-"+command.Label)
			matches, err := remoteGlob(cfg.Server, pattern)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: glob %s: %v\n", pattern, err)
				continue
			}

			for _, worktreeDir := range matches {
				totals, err := readWorktreeTokens(cfg.Server, homeDir, worktreeDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: read tokens for %s: %v\n", worktreeDir, err)
					continue
				}
				totals.label = command.Label
				totals.worktree = filepath.Base(worktreeDir)
				if evaluate {
					totals.dir = worktreeDir
					totals.server = cfg.Server
					totals.homeDir = homeDir
					totals.parent = parent
					totals.claudeCmd = command.Cmd
				}
				rows = append(rows, totals)
			}
		}

		if evaluate && len(rows) > cfgRowStart {
			// cfgRows is a slice of the same backing array — mutations propagate to rows.
			cfgRows := rows[cfgRowStart:]
			runGroupedEvaluations(cfgRows)
		}
	}

	// Print TSV header then rows.
	if evaluate {
		fmt.Println("label\tworktree\tsessions\tinput_tokens\toutput_tokens\tcache_read_tokens\tcache_creation_tokens\tproblem\tclarity\tcomplexity\tcorrect\tcomplete\tmissing")
		for _, r := range rows {
			correctStr := ""
			if r.eval.Correct != nil {
				correctStr = fmt.Sprintf("%v", *r.eval.Correct)
			}
			fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
				r.label, r.worktree, r.sessions,
				r.inputTokens, r.outputTokens, r.cacheReadTokens, r.cacheCreationTokens,
				r.eval.Problem, r.eval.Clarity, r.eval.Complexity,
				correctStr, r.complete, r.eval.Missing)
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

	for _, indices := range groups {
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

			prompt := buildEvalPrompt(target, others)
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
				fmt.Fprintf(os.Stderr, "warning: parse evaluation output for %s: %v\n", target.worktree, err)
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

func buildEvalPrompt(target tokenTotals, others []tokenTotals) string {
	targetProjectsDir := claudeProjectsDir(target.homeDir, target.dir)

	var sb strings.Builder
	sb.WriteString("You are evaluating a software implementation. You are running in the parent\n")
	sb.WriteString("directory; do NOT cd into any worktree — that would create a new Claude session\n")
	sb.WriteString("history for that directory and contaminate its token counts.\n\n")

	sb.WriteString("The implementation you are evaluating:\n")
	fmt.Fprintf(&sb, "  Directory:         %s\n", target.dir)
	fmt.Fprintf(&sb, "  Label:             %s\n", target.label)
	fmt.Fprintf(&sb, "  Conversation dir:  %s\n", targetProjectsDir)
	sb.WriteString("  (find session: ls -t <above>/*.jsonl | head -1)\n\n")

	if len(others) > 0 {
		sb.WriteString("Other implementations of the same task:\n")
		for _, o := range others {
			oProjectsDir := claudeProjectsDir(o.homeDir, o.dir)
			fmt.Fprintf(&sb, "  - %s  (label: %s)\n", o.dir, o.label)
			fmt.Fprintf(&sb, "    Conversation dir: %s\n", oProjectsDir)
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`Steps (use git -C <dir> flags instead of cd to avoid changing your working directory):
1. For THIS implementation and each other implementation:
   a. Review changes: git -C <dir> log --oneline HEAD
   b. Review the diff: git -C <dir> diff HEAD~<n>
   c. Read the most recent conversation:
      ls -t <conversationDir>/*.jsonl | head -1 | xargs head -c 200000
2. Compare all implementations.

Output ONLY the following JSON block, then a blank line, then a prose description:

{
  "problem": "debug" or "feature",
  "clarity": "well-specified" or "ambiguous",
  "complexity": "easy" or "moderate" or "hard",
  "correct": true or false,
  "all_aspects": "comma-separated list of ALL implementation aspects seen across ALL directories",
  "implemented": "comma-separated aspects present in THIS directory",
  "missing": "comma-separated aspects absent from THIS directory"
}

After the JSON, write a succinct prose description of notable differences in THIS
implementation only. Leave assessment of the other directories to their own evaluators.
`)
	return sb.String()
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

func runEvaluation(server, parent, worktreeBase, claudeCmd string) (string, error) {
	promptPath := fmt.Sprintf("/tmp/clorchestrate-eval-%s.md", worktreeBase)
	shellCmd := fmt.Sprintf(`cd %s && %s --print "$(cat %s)"`, parent, claudeCmd, promptPath)

	var cmd *exec.Cmd
	if server == "" {
		cmd = exec.Command("sh", "-c", shellCmd)
	} else {
		cmd = exec.Command("ssh", server, shellCmd)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run evaluation in %s: %w", parent, err)
	}
	return stdout.String(), nil
}

// extractJSONAndProse finds the JSON object in output (stripping markdown code fences and
// surrounding prose) and returns it along with any trailing prose description.
func extractJSONAndProse(output string) (string, string, error) {
	s := strings.TrimSpace(output)

	// Handle markdown code fences (```json...``` or ```...```)
	if fenceIdx := strings.Index(s, "```"); fenceIdx != -1 {
		inner := s[fenceIdx+3:]
		// skip optional language tag line (e.g. "json\n")
		if nl := strings.Index(inner, "\n"); nl != -1 {
			inner = inner[nl+1:]
		}
		if closeIdx := strings.Index(inner, "```"); closeIdx != -1 {
			candidate := strings.TrimSpace(inner[:closeIdx])
			prose := strings.TrimSpace(inner[closeIdx+3:])
			if start := strings.Index(candidate, "{"); start != -1 {
				if end := strings.LastIndex(candidate, "}"); end > start {
					jsonStr := candidate[start : end+1]
					if json.Valid([]byte(jsonStr)) {
						return jsonStr, prose, nil
					}
				}
			}
		}
	}

	// Fall back: find outermost { ... } in raw output
	start := strings.Index(s, "{")
	if start == -1 {
		return "", "", fmt.Errorf("no JSON object found in evaluation output")
	}
	end := strings.LastIndex(s, "}")
	if end <= start {
		return "", "", fmt.Errorf("no closing } found in evaluation output")
	}
	jsonStr := s[start : end+1]
	if !json.Valid([]byte(jsonStr)) {
		return "", "", fmt.Errorf("invalid JSON in evaluation output")
	}
	prose := strings.TrimSpace(s[end+1:])
	return jsonStr, prose, nil
}

// computeCompleteness merges all_aspects across a task group and sets each row's complete
// field to the percentage of global aspects that row's implementation covers.
func computeCompleteness(rows []tokenTotals, indices []int) {
	globalAspects := map[string]bool{}
	for _, i := range indices {
		for _, a := range splitAspects(rows[i].eval.AllAspects) {
			globalAspects[a] = true
		}
	}
	total := len(globalAspects)
	if total == 0 {
		return
	}
	for _, i := range indices {
		count := 0
		for _, a := range splitAspects(rows[i].eval.Implemented) {
			if globalAspects[a] {
				count++
			}
		}
		rows[i].complete = count * 100 / total
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
