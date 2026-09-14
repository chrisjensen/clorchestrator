package github

import (
	"fmt"
	"os/exec"
	"strings"
)

type DevelopArgs struct {
	IssueNum         string
	IssueRepo        string
	BranchRepo       string
	Base             string
	BranchNameFormat string // tokens {issue} {handle} replaced before calling gh
	Handle           string
	BranchName       string // explicit branch name; overrides BranchNameFormat when set
}

// runFunc is the subprocess runner. Replaced in tests.
var runFunc = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// DevelopBranch runs `gh issue develop` and returns the branch name.
func DevelopBranch(a DevelopArgs) (string, error) {
	args := []string{
		"issue", "develop", a.IssueNum,
		"--repo", a.IssueRepo,
		"--branch-repo", a.BranchRepo,
		"--base", a.Base,
	}
	if a.BranchName != "" {
		args = append(args, "--name", a.BranchName)
	} else if a.BranchNameFormat != "" {
		args = append(args, "--name", FormatBranchName(a.BranchNameFormat, a.IssueNum, a.Handle))
	}
	out, err := runFunc("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh issue develop: %w: %s", err, string(out))
	}
	branch, ok := ParseBranchFromOutput(string(out))
	if !ok {
		return "", fmt.Errorf("could not parse branch name from gh output: %s", string(out))
	}
	return branch, nil
}

// DefaultBranch returns the repo's default branch (e.g. "main", "master") as
// reported by GitHub.
func DefaultBranch(repo string) (string, error) {
	out, err := runFunc("gh", "repo", "view", repo, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	if err != nil {
		return "", fmt.Errorf("gh repo view %s: %w: %s", repo, err, string(out))
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("gh repo view %s: empty default branch", repo)
	}
	return branch, nil
}

// FormatBranchName substitutes {issue} and {handle} tokens.
func FormatBranchName(format, issue, handle string) string {
	s := strings.ReplaceAll(format, "{issue}", issue)
	s = strings.ReplaceAll(s, "{handle}", handle)
	return s
}

// ListLinkedBranches returns branch names already linked to the given issue.
func ListLinkedBranches(issueNum, issueRepo string) ([]string, error) {
	out, err := runFunc("gh", "issue", "develop", issueNum, "--repo", issueRepo, "--list")
	if err != nil {
		return nil, fmt.Errorf("gh issue develop --list: %w: %s", err, string(out))
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if b, ok := ParseBranchFromOutput(line); ok {
			branches = append(branches, b)
		}
	}
	return branches, nil
}

// IsIssueClosed returns true when the issue's state is not OPEN.
func IsIssueClosed(issueNum, issueRepo string) (bool, error) {
	out, err := runFunc("gh", "issue", "view", issueNum, "--repo", issueRepo, "--json", "state", "--jq", ".state")
	if err != nil {
		return false, fmt.Errorf("gh issue view: %w: %s", err, string(out))
	}
	state := strings.TrimSpace(string(out))
	return state != "OPEN", nil
}

// ParseBranchFromOutput extracts the branch name from `gh issue develop` output,
// which typically prints a URL ending in the branch: .../tree/<branch-name>
func ParseBranchFromOutput(out string) (string, bool) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		idx := strings.LastIndexByte(line, '/')
		if idx < 0 || idx == len(line)-1 {
			continue
		}
		return line[idx+1:], true
	}
	return "", false
}
