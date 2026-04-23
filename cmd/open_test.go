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

func TestBuildRemoteCmd_FullTask(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon")
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle") {
		t.Errorf("missing screen -S with config prefix: %s", got)
	}
	if !strings.Contains(got, "~/bin/start-task.sh my-handle") {
		t.Errorf("missing start-task call with bare handle: %s", got)
	}
}

func TestBuildRemoteCmd_BareSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeBareSession, "", "mycon")
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd: %s", got)
	}
	if strings.Contains(got, "screen") {
		t.Errorf("bare session should not use screen: %s", got)
	}
}

func TestBuildRemoteCmd_HandleSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeHandleSession, "my-handle", "mycon")
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle") {
		t.Errorf("missing screen -S with config prefix: %s", got)
	}
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd to remote repo: %s", got)
	}
	if strings.Contains(got, "start-task.sh") {
		t.Errorf("handle session should not call start-task.sh: %s", got)
	}
}
