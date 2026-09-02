package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpecRoundTripAndGuestFiles(t *testing.T) {
	dir := t.TempDir()
	input := Spec{
		Image:       "docker.io/library/alpine",
		ImageDigest: "sha256:abc",
		Environment: []string{"PATH=/bin"},
		WorkingDir:  "/workspace",
		UserCommand: "echo ready",
		MountHost:   "/tmp/project",
		MountGuest:  "/workspace",
		ParentName:  "parent",
	}
	if err := Save(dir, input); err != nil {
		t.Fatal(err)
	}
	output, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if output.ImageDigest != input.ImageDigest || output.ParentName != input.ParentName || output.MountGuest != input.MountGuest {
		t.Fatalf("workspace spec round trip = %#v", output)
	}

	for name, want := range map[string]string{
		userCommandName:  "echo ready",
		imageWorkdirName: "/workspace\n",
		mountGuestName:   "/workspace\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
}

func TestSaveUsesDefaultWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Spec{Image: "alpine", ImageDigest: "sha256:abc"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, imageWorkdirName))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != defaultWorkdir+"\n" {
		t.Fatalf("default workdir = %q, want %q", data, defaultWorkdir+"\n")
	}
}

func TestMarkRegenerateIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := MarkRegenerateIdentity(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, regenerateIdentityName)); err != nil {
		t.Fatal(err)
	}
}
