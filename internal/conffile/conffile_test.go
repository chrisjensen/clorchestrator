package conffile

import (
	"strings"
	"testing"
)

func TestRender_Mode1(t *testing.T) {
	got := Render(TaskConf{
		Branch:          "feature/123-test",
		RemoteRepo:      "~/src/repo",
		Issue:           "123",
		IssueRepo:       "org/repo",
		PostSetupCmd:    "npm run build",
		PlanningContext: "context here",
		ExtraContext:    "extra notes",
	})
	mustContain(t, got, "ISSUE=123")
	mustContain(t, got, "BRANCH=feature/123-test")
	mustContain(t, got, "REMOTE_REPO=~/src/repo")
	mustContain(t, got, "ISSUE_REPO=org/repo")
	mustContain(t, got, "POST_SETUP_CMD='npm run build'")
	mustContain(t, got, "PLANNING_CONTEXT='context here'")
	mustContain(t, got, "EXTRA_CONTEXT='extra notes'")
}

func TestRender_Mode2(t *testing.T) {
	got := Render(TaskConf{
		Branch:       "feature/x",
		RemoteRepo:   "~/src/repo",
		PostSetupCmd: "npm run build",
	})
	mustContain(t, got, "BRANCH=feature/x")
	mustContain(t, got, "REMOTE_REPO=~/src/repo")
	mustContain(t, got, "POST_SETUP_CMD='npm run build'")

	for _, forbidden := range []string{"ISSUE=", "ISSUE_REPO=", "PLANNING_CONTEXT=", "EXTRA_CONTEXT="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("Mode 2 output should not contain %q:\n%s", forbidden, got)
		}
	}
}

func TestShellQuote_EscapesSingleQuote(t *testing.T) {
	got := shellQuote("it's a test")
	want := `'it'\''s a test'`
	if got != want {
		t.Errorf("shellQuote = %q, want %q", got, want)
	}
}

func mustContain(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("missing %q in output:\n%s", want, got)
	}
}
