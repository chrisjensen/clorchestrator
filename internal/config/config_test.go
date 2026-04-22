package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParse_AllKeys(t *testing.T) {
	path := writeTemp(t, `
DISPATCH_SERVER=myserver
DISPATCH_REMOTE_REPO=~/src/myrepo
DISPATCH_ISSUE_REPO=org/issues
DISPATCH_BRANCH_REPO=org/branches
DISPATCH_DEFAULT_BASE=develop
DISPATCH_BRANCH_NAME_FORMAT=feature/{issue}-{handle}
DISPATCH_POST_SETUP_CMD="npm run build"
DISPATCH_ITERM_TAB_COLOR="#f4b6cf"
DISPATCH_PLANNING_CONTEXT="some context"
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "myserver" {
		t.Errorf("Server = %q, want myserver", cfg.Server)
	}
	if cfg.RemoteRepo != "~/src/myrepo" {
		t.Errorf("RemoteRepo = %q", cfg.RemoteRepo)
	}
	if cfg.IssueRepo != "org/issues" {
		t.Errorf("IssueRepo = %q", cfg.IssueRepo)
	}
	if cfg.BranchRepo != "org/branches" {
		t.Errorf("BranchRepo = %q", cfg.BranchRepo)
	}
	if cfg.DefaultBase != "develop" {
		t.Errorf("DefaultBase = %q", cfg.DefaultBase)
	}
	if cfg.BranchNameFormat != "feature/{issue}-{handle}" {
		t.Errorf("BranchNameFormat = %q", cfg.BranchNameFormat)
	}
	if cfg.PostSetupCmd != "npm run build" {
		t.Errorf("PostSetupCmd = %q", cfg.PostSetupCmd)
	}
	if cfg.ITermTabColor != "#f4b6cf" {
		t.Errorf("ITermTabColor = %q", cfg.ITermTabColor)
	}
	if cfg.PlanningContext != "some context" {
		t.Errorf("PlanningContext = %q", cfg.PlanningContext)
	}
}

func TestParse_QuotedValues(t *testing.T) {
	path := writeTemp(t, `
DISPATCH_SERVER="my server"
DISPATCH_ITERM_TAB_COLOR='#FF8800'
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "my server" {
		t.Errorf("Server = %q, want 'my server'", cfg.Server)
	}
	if cfg.ITermTabColor != "#FF8800" {
		t.Errorf("ITermTabColor = %q", cfg.ITermTabColor)
	}
}

func TestParse_DefaultsAndComments(t *testing.T) {
	path := writeTemp(t, `
# A comment
DISPATCH_SERVER=myserver

# Another comment
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBase != "main" {
		t.Errorf("DefaultBase = %q, want main", cfg.DefaultBase)
	}
	if cfg.Server != "myserver" {
		t.Errorf("Server = %q", cfg.Server)
	}
}
