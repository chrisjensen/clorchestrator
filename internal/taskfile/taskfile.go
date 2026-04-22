package taskfile

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type Task struct {
	Handle       string
	IssueNum     string
	BaseBranch   string
	ExtraContext string
}

var (
	headingRE = regexp.MustCompile(`^##\s+(.+)$`)
	baseRE    = regexp.MustCompile(`^base:\s*(.+)$`)
	// Matches: URL form, org/repo#NNN shorthand, or bare #NNN.
	// We take the last number found in the match.
	issueURLRE       = regexp.MustCompile(`https?://github\.com/[^/\s]+/[^/\s]+/issues/(\d+)`)
	issueShorthandRE = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#(\d+)`)
	issueBareRE      = regexp.MustCompile(`#(\d+)`)
)

func Parse(path string) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open task file %s: %w", path, err)
	}
	defer f.Close()

	var tasks []Task
	var curHandle string
	var curBody strings.Builder

	flush := func() {
		if curHandle == "" {
			return
		}
		body := curBody.String()
		task, ok := buildTask(curHandle, body)
		if ok {
			tasks = append(tasks, task)
		} else {
			fmt.Fprintf(os.Stderr, "No issue ref found for %q, skipping\n", curHandle)
		}
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush()
			curHandle = strings.TrimSpace(m[1])
			curBody.Reset()
			continue
		}
		curBody.WriteString(line)
		curBody.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read task file %s: %w", path, err)
	}
	flush()
	return tasks, nil
}

func buildTask(handle, body string) (Task, bool) {
	issueNum := ExtractIssueNum(body)
	if issueNum == "" {
		return Task{}, false
	}

	var base string
	var contextLines []string
	for _, line := range strings.Split(body, "\n") {
		if m := baseRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			base = strings.TrimSpace(m[1])
			continue
		}
		contextLines = append(contextLines, line)
	}

	extra := strings.TrimSpace(strings.Join(contextLines, "\n"))
	return Task{
		Handle:       handle,
		IssueNum:     issueNum,
		BaseBranch:   base,
		ExtraContext: extra,
	}, true
}

// ExtractIssueNum returns the issue number from a body that may contain a GitHub
// URL, an org/repo#N shorthand, or a bare #N. Returns "" if none found.
func ExtractIssueNum(s string) string {
	if m := issueURLRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := issueShorthandRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := issueBareRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// NormalizeIssueArg accepts a bare number, #NNN, org/repo#NNN, or a GitHub
// issue URL and returns the bare number.
func NormalizeIssueArg(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("empty issue")
	}
	if num := ExtractIssueNum(arg); num != "" {
		return num, nil
	}
	// Accept bare number
	if matched, _ := regexp.MatchString(`^\d+$`, arg); matched {
		return arg, nil
	}
	return "", fmt.Errorf("could not parse issue from %q", arg)
}
