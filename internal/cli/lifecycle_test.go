package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKernelPath(t *testing.T) {
	if path, err := resolveKernelPath(""); err != nil || path != "" {
		t.Fatalf("resolve empty kernel path = %q, %v", path, err)
	}

	dir := t.TempDir()
	kernel := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernel, []byte("not validated by design"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := resolveKernelPath(kernel)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("resolved kernel path = %q, want %q", path, want)
	}

	for _, value := range []string{filepath.Join(dir, "missing"), dir} {
		if _, err := resolveKernelPath(value); err == nil {
			t.Errorf("resolveKernelPath(%q) unexpectedly succeeded", value)
		}
	}
}

func TestValidatePorts(t *testing.T) {
	if err := validatePorts([]int{80, 443, 8080}); err != nil {
		t.Fatal(err)
	}
	for _, ports := range [][]int{{0}, {65536}, {8080, 8080}} {
		if err := validatePorts(ports); err == nil {
			t.Errorf("validatePorts(%v) unexpectedly succeeded", ports)
		}
	}
}

func TestParseMount(t *testing.T) {
	dir := t.TempDir()
	host, guest, err := parseMount(dir + ":/workspace")
	if err != nil {
		t.Fatal(err)
	}
	wantHost, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if host != wantHost || guest != "/workspace" {
		t.Fatalf("parseMount = %q, %q", host, guest)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"missing", dir + ":relative", dir + ":/devd", file + ":/workspace"} {
		if _, _, err := parseMount(value); err == nil {
			t.Errorf("parseMount(%q) unexpectedly succeeded", value)
		}
	}
}
