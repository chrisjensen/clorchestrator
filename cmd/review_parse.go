package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// readWorktreeTokens reads every Claude Code JSONL transcript for a worktree
// and sums their token usage. Evaluation runs in the parent dir rather than
// the worktree itself, so every session found here belongs to the
// implementation. Sessions counts all JSONL files present so the caller can
// see how many attempts existed.
func readWorktreeTokens(server, homeDir, worktreeDir string) (tokenTotals, error) {
	projectDir := claudeProjectsDir(homeDir, worktreeDir)
	shellCmd := fmt.Sprintf("cat %s/*.jsonl 2>/dev/null", projectDir)

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
