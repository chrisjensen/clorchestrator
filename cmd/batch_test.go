package cmd

import (
	"strings"
	"testing"
)

func TestAheadShellCmd_TildePathUnquoted(t *testing.T) {
	got := aheadShellCmd("~/src/ncoderz/extractor-9802-foo", "main")
	if strings.Contains(got, `"~`) || strings.Contains(got, `'~`) {
		t.Fatalf("path got quoted, breaks tilde expansion: %s", got)
	}
	if !strings.Contains(got, "[ -d ~/src/ncoderz/extractor-9802-foo ]") {
		t.Errorf("missing unquoted tilde path: %s", got)
	}
	if !strings.Contains(got, "git -C ~/src/ncoderz/extractor-9802-foo rev-list --count origin/main..HEAD") {
		t.Errorf("missing git rev-list invocation: %s", got)
	}
	if !strings.Contains(got, "echo MISSING") {
		t.Errorf("missing MISSING short-circuit: %s", got)
	}
}
