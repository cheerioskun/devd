package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"devd/internal/config"
)

func TestSpecRoundTripDoesNotRenderGuestFiles(t *testing.T) {
	dir := t.TempDir()
	input := Spec{
		Version:     config.WorkspaceSpecVersion,
		Environment: []string{"PATH=/bin"}, WorkingDir: "/workspace",
		UserCommand: "echo ready", MountHost: "/tmp/project", MountGuest: "/workspace",
		KernelPath: "/tmp/kernel",
	}
	if err := Save(dir, input); err != nil {
		t.Fatal(err)
	}
	output, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, *output) {
		t.Fatalf("round trip = %#v, want %#v", output, input)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != specName {
		t.Fatalf("Save rendered non-authoritative files: %v", entries)
	}
}

func TestControlIsDisposableAndScoped(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{UserCommand: "echo ready", MountGuest: "/workspace", KernelPath: "/private/kernel"}
	if err := Save(dir, spec); err != nil {
		t.Fatal(err)
	}
	if err := MarkRegenerateIdentity(dir); err != nil {
		t.Fatal(err)
	}
	control, err := PrepareControl(dir, spec, "public-key\n")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		userCommandName: "echo ready", imageWorkdirName: defaultWorkdir + "\n",
		mountGuestName: "/workspace\n", "authorized_keys": "public-key\n",
		regenerateIdentityName: "",
	} {
		data, err := os.ReadFile(filepath.Join(control, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	entries, err := os.ReadDir(control)
	if err != nil || len(entries) != 5 {
		t.Fatalf("unexpected control contents: %v, %v", entries, err)
	}
	// A previous guest's symlink must not redirect a host write into its spec.
	if err := os.Remove(filepath.Join(control, userCommandName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, specName), filepath.Join(control, userCommandName)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareControl(dir, spec, "replacement-key\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("render followed a guest symlink: %v", err)
	}
	// Merely rendering or retrying boot does not acknowledge first-boot work.
	if _, err := os.Stat(filepath.Join(dir, regenerateIdentityName)); err != nil {
		t.Fatal(err)
	}
	if err := CompleteBoot(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareControl(dir, spec, "public-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(control, regenerateIdentityName)); !os.IsNotExist(err) {
		t.Fatalf("successful boot retained identity request: %v", err)
	}
}

func TestLoadRejectsUnsupportedSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, specName), []byte(`{"format_version":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted unsupported spec")
	}
}
