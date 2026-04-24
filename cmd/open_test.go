package cmd

import (
	"strings"
	"testing"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

func TestDetectMode(t *testing.T) {
	cases := []struct {
		name    string
		handle  string
		branch  string
		issue   string
		want    Mode
		wantErr bool
	}{
		{"bare", "", "", "", ModeBareSession, false},
		{"worktree", "h", "b", "", ModeWorktree, false},
		{"full", "h", "b", "42", ModeFullTask, false},
		{"handle only", "h", "", "", ModeHandleSession, false},
		{"invalid issue only", "", "", "42", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := detectMode(c.handle, c.branch, c.issue)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && m != c.want {
				t.Errorf("got %v, want %v", m, c.want)
			}
		})
	}
}

func TestBuildRemoteCmd_FullTaskFresh(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "")
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle bash -l") {
		t.Errorf("expected plain screen+bash -l: %s", got)
	}
	if strings.Contains(got, "start-task.sh") {
		t.Errorf("should not reference start-task.sh: %s", got)
	}
}

func TestBuildRemoteCmd_FullTaskReattach(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "12345.mycon_my-handle")
	if !strings.Contains(got, "screen -r 12345.mycon_my-handle") {
		t.Errorf("expected screen -r for reattach: %s", got)
	}
	if strings.Contains(got, "screen -dr") {
		t.Errorf("reattach should not force-detach with -dr: %s", got)
	}
}

func TestBuildRemoteCmd_BareSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeBareSession, "", "", "")
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd: %s", got)
	}
	if strings.Contains(got, "screen") {
		t.Errorf("bare session should not use screen: %s", got)
	}
}

func TestBuildRemoteCmd_HandleSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeHandleSession, "my-handle", "mycon_my-handle", "")
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle") {
		t.Errorf("missing screen -S with config prefix: %s", got)
	}
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd to remote repo: %s", got)
	}
}

func TestBuildFollowupCmd(t *testing.T) {
	wd := "~/src/extractor-branch-x"
	if got := buildFollowupCmd(ModeFullTask, "h", wd, ""); !strings.Contains(got, "cd ~/src/extractor-branch-x && claude --permission-mode plan \"$(cat /tmp/task-h.prompt.md)\"") {
		t.Errorf("ModeFullTask fresh: got %q", got)
	}
	if got := buildFollowupCmd(ModeWorktree, "h", wd, ""); got != "cd ~/src/extractor-branch-x && claude" {
		t.Errorf("ModeWorktree fresh: got %q", got)
	}
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "123.x"); got != "" {
		t.Errorf("reattach should suppress followup: got %q", got)
	}
	if got := buildFollowupCmd(ModeHandleSession, "h", wd, ""); got != "" {
		t.Errorf("ModeHandleSession should have no followup: got %q", got)
	}
}

func TestWorktreePath(t *testing.T) {
	// Mirrors scripts/worktree-checkout.sh: $(dirname REMOTE_REPO)/extractor-<sanitized-branch>
	cases := []struct {
		repo, branch, want string
	}{
		{"~/src/ncoderz/extractor", "9802-foo", "~/src/ncoderz/extractor-9802-foo"},
		{"~/src/ncoderz/extractor/", "9802-foo", "~/src/ncoderz/extractor-9802-foo"},
		{"~/src/ncoderz/extractor", "feature/abc", "~/src/ncoderz/extractor-feature-abc"},
		{"/home/chris/src/repo", "x/y/z", "/home/chris/src/extractor-x-y-z"},
	}
	for _, c := range cases {
		if got := worktreePath(c.repo, c.branch); got != c.want {
			t.Errorf("worktreePath(%q, %q) = %q, want %q", c.repo, c.branch, got, c.want)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	got := buildPrompt("42", "org/repo", "ctx-here", "extra-here")
	for _, want := range []string{
		"issue #42 in org/repo",
		"https://github.com/org/repo/issues/42",
		"ctx-here",
		"extra-here",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}
