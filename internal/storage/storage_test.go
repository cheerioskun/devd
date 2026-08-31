package storage

import "testing"

func TestQualifyImage(t *testing.T) {
	tests := map[string]string{
		"alpine":                          "docker.io/library/alpine",
		"nicolaka/netshoot":               "docker.io/nicolaka/netshoot",
		"ghcr.io/example/image:latest":    "ghcr.io/example/image:latest",
		"localhost:5000/example/image:v1": "localhost:5000/example/image:v1",
		"oci-archive:/tmp/image.tar":      "oci-archive:/tmp/image.tar",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := QualifyImage(input); got != want {
				t.Fatalf("QualifyImage(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestWorkspaceConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	input := WorkspaceConfig{
		Image:       "docker.io/library/alpine",
		ImageDigest: "sha256:abc",
		Environment: []string{"PATH=/bin"},
		WorkingDir:  "/root",
		UserCommand: "echo ready",
		MountHost:   "/tmp/project",
		MountGuest:  "/workspace",
		ParentName:  "parent",
	}
	if err := WriteWorkspaceConfig(dir, input); err != nil {
		t.Fatal(err)
	}
	output, err := ReadWorkspaceConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if output.ImageDigest != input.ImageDigest || output.ParentName != input.ParentName || output.MountGuest != input.MountGuest {
		t.Fatalf("workspace config round trip = %#v", output)
	}
}
