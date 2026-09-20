package scripts

import (
	"strings"
	"testing"
)

func TestEmbed_NonEmpty(t *testing.T) {
	all := All()
	if len(all) != 2 {
		t.Fatalf("got %d scripts, want 2", len(all))
	}
	for _, s := range all {
		if len(s.Content) == 0 {
			t.Errorf("%s: empty content", s.Name)
		}
		if !strings.HasPrefix(string(s.Content), "#!/usr/bin/env bash") {
			t.Errorf("%s: missing bash shebang", s.Name)
		}
	}
}
