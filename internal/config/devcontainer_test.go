package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDevContainerJSONC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	contents := `{
  // URLs inside strings are not comments.
  "image": "registry.example/dev/image:latest", // trailing comment
  "forwardPorts": [3000, 8080,],
  "postCreateCommand": "curl https://example.com/setup", /* block comment */
}
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	devContainer, err := LoadDevContainer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if devContainer == nil {
		t.Fatal("LoadDevContainer returned nil")
	}
	if devContainer.Image != "registry.example/dev/image:latest" {
		t.Errorf("image = %q", devContainer.Image)
	}
	if len(devContainer.ForwardPorts) != 2 || devContainer.ForwardPorts[1] != 8080 {
		t.Errorf("forwardPorts = %v", devContainer.ForwardPorts)
	}
	if devContainer.PostCreateCommand != "curl https://example.com/setup" {
		t.Errorf("postCreateCommand = %q", devContainer.PostCreateCommand)
	}
}

func TestLoadDevContainerReportsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(`{"image":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDevContainer(dir); err == nil {
		t.Fatal("LoadDevContainer unexpectedly accepted invalid JSON")
	}
}
