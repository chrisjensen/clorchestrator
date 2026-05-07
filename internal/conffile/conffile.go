package conffile

import (
	"fmt"
	"strings"
)

type TaskConf struct {
	Branch          string
	RemoteRepo      string
	WorktreePrefix  string
	Issue           string
	IssueRepo       string
	PostSetupCmd    string
	PlanningContext string
	ExtraContext    string
}

// Render returns the /tmp/task-<handle>.conf content for a Mode 1 (full task)
// or Mode 2 (worktree, no issue) session. Mode 2 is detected by Issue == "".
//
// Text fields that may contain whitespace or shell metacharacters are wrapped
// in single quotes (with embedded single quotes escaped). This matches the
// safety of the original bash `printf '%q'` usage for values that will be
// re-sourced by start-task.sh.
func Render(c TaskConf) string {
	var b strings.Builder
	if c.Issue != "" {
		fmt.Fprintf(&b, "ISSUE=%s\n", c.Issue)
	}
	fmt.Fprintf(&b, "BRANCH=%s\n", c.Branch)
	fmt.Fprintf(&b, "REMOTE_REPO=%s\n", c.RemoteRepo)
	if c.WorktreePrefix != "" {
		fmt.Fprintf(&b, "WORKTREE_PREFIX=%s\n", c.WorktreePrefix)
	}
	if c.IssueRepo != "" {
		fmt.Fprintf(&b, "ISSUE_REPO=%s\n", c.IssueRepo)
	}
	fmt.Fprintf(&b, "POST_SETUP_CMD=%s\n", shellQuote(c.PostSetupCmd))
	if c.Issue != "" {
		fmt.Fprintf(&b, "PLANNING_CONTEXT=%s\n", shellQuote(c.PlanningContext))
		fmt.Fprintf(&b, "EXTRA_CONTEXT=%s\n", shellQuote(c.ExtraContext))
	}
	return b.String()
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes
// using the standard bash pattern: 'it'\''s'
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
