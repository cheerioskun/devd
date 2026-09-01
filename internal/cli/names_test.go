package cli

import (
	"regexp"
	"testing"
)

func TestWorkspaceNameBase(t *testing.T) {
	tests := map[string]string{
		"ubuntu":                                "ubuntu",
		"ubuntu:24.04":                          "ubuntu",
		"docker.io/library/ubuntu:24.04":        "ubuntu",
		"ghcr.io/example/dev-image@sha256:abcd": "dev-image",
		"oci-archive:/tmp/My Image.tar":         "my-image-tar",
		"!!!":                                   "workspace",
	}
	for input, want := range tests {
		if got := workspaceNameBase(input); got != want {
			t.Errorf("workspaceNameBase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGeneratedWorkspaceName(t *testing.T) {
	name, err := generatedWorkspaceName("ubuntu:24.04")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^ubuntu-[0-9a-f]{6}$`).MatchString(name) {
		t.Fatalf("generated name %q has unexpected form", name)
	}
}
