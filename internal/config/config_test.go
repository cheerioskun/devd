package config

import "testing"

func TestValidateWorkspaceName(t *testing.T) {
	for _, name := range []string{"app", "app-1", "team.api", "under_score"} {
		if err := ValidateWorkspaceName(name); err != nil {
			t.Errorf("ValidateWorkspaceName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", "../escape", "a/b", " space", "name:tag"} {
		if err := ValidateWorkspaceName(name); err == nil {
			t.Errorf("ValidateWorkspaceName(%q) unexpectedly succeeded", name)
		}
	}
}
