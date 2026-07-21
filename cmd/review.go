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
	return &cobra.Command{
		Use:   "review [config]",
		Short: "review token usage across benchmark sessions",
		Long: `Scan benchmark worktrees and report token usage per label as TSV.

Without a config argument all configs in ~/.clorchestrate/ are scanned.
With a config argument only that config's worktrees are checked.

Benchmark worktrees are directories whose name ends with a command label
defined in the config's [[command]] blocks. Token data is read from the
Claude Code JSONL transcript files stored under ~/.claude/projects/ on the
machine where the sessions ran.`,
		Args:              cobra.RangeArgs(0, 1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var configArg string
			if len(args) > 0 {
				configArg = args[0]
			}
			return reviewRun(configArg)
		},
	}
}

type tokenTotals struct {
	label                    string
	worktree                 string
	sessions                 int
	inputTokens              int
	outputTokens             int
	cacheReadTokens          int
	cacheCreationTokens      int
}

func reviewRun(configArg string) error {
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

		for _, cmd := range cfg.Commands {
			// Glob for worktrees ending with the label suffix.
			pattern := filepath.Join(parent, prefix+"-*-"+cmd.Label)
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
				totals.label = cmd.Label
				totals.worktree = filepath.Base(worktreeDir)
				rows = append(rows, totals)
			}
		}
	}

	// Print TSV header then rows.
	fmt.Println("label\tworktree\tsessions\tinput_tokens\toutput_tokens\tcache_read_tokens\tcache_creation_tokens")
	for _, r := range rows {
		fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			r.label, r.worktree, r.sessions,
			r.inputTokens, r.outputTokens, r.cacheReadTokens, r.cacheCreationTokens)
	}
	return nil
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

// readWorktreeTokens reads Claude Code JSONL transcripts for a worktree and
// sums token usage. Sessions are counted as distinct JSONL files.
func readWorktreeTokens(server, homeDir, worktreeDir string) (tokenTotals, error) {
	projectDir := claudeProjectsDir(homeDir, worktreeDir)
	shellCmd := fmt.Sprintf("ls %s/*.jsonl 2>/dev/null | xargs -r cat", projectDir)

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
