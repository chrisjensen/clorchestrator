package cmd

import (
	"strings"
	"testing"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

func TestDetectMode(t *testing.T) {
	cases := []struct {
		name         string
		handle       string
		branch       string
		issue        string
		want         Mode
		wantErr      bool
	}{
		{"bare", "", "", "", ModeBareSession, false},
		{"worktree", "h", "b", "", ModeWorktree, false},
		{"full", "h", "b", "42", ModeFullTask, false},
		{"invalid handle only", "h", "", "", 0, true},
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

func TestBuildRemoteCmd_FullTask(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle")
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S my-handle") {
		t.Errorf("missing screen -S: %s", got)
	}
	if !strings.Contains(got, "~/bin/start-task.sh my-handle") {
		t.Errorf("missing start-task call: %s", got)
	}
}

func TestBuildRemoteCmd_BareSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeBareSession, "")
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd: %s", got)
	}
	if strings.Contains(got, "screen") {
		t.Errorf("bare session should not use screen: %s", got)
	}
}
