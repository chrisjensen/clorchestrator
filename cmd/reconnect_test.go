package cmd

import "testing"

func TestRunKeyAndLabel(t *testing.T) {
	labels := []string{"a", "b"}

	cases := []struct {
		name        string
		wantRunKey  string
		wantGrouped bool
	}{
		{"myconfig_task123_a", "myconfig_task123", true},
		{"myconfig_task123_b", "myconfig_task123", true},
		{"myconfig_task123_coordinator", "myconfig_task123", true},
		{"myconfig_task123", "myconfig_task123", false},
		{"myconfig_task123_ab", "myconfig_task123_ab", false},
	}
	for _, c := range cases {
		runKey, grouped := runKeyAndLabel(c.name, labels)
		if runKey != c.wantRunKey || grouped != c.wantGrouped {
			t.Errorf("runKeyAndLabel(%q) = (%q, %v), want (%q, %v)", c.name, runKey, grouped, c.wantRunKey, c.wantGrouped)
		}
	}
}
