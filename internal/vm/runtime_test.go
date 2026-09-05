package vm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fakeRuntime(t *testing.T) StartOpts {
	t.Helper()
	dir := t.TempDir()
	runtime := filepath.Join(dir, "devd-vm")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVD_VM_RUNTIME", runtime)
	return StartOpts{
		DiskPath: filepath.Join(dir, "disk with spaces.ext4"),
		Command:  "/bin/sh", Args: []string{"-c", "echo guest"}, CPUs: 2, Memory: 512,
		KernelPath: "/a kernel/Image", Env: []string{"PATH=/bin"},
		Mounts: []Mount{{Tag: "devd", HostPath: "/a control directory"}}, Workdir: "/",
		LogFile: filepath.Join(dir, "vm.log"), ProcessFile: filepath.Join(dir, "process.json"),
	}
}

func TestStartRecordsIdentityAndExactArguments(t *testing.T) {
	opts := fakeRuntime(t)
	process, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Stop(StopOpts{Process: process}); err != nil {
			t.Error(err)
		}
	})
	receipt, err := ReadProcess(opts.ProcessFile)
	if err != nil || receipt == nil || *receipt != process {
		t.Fatalf("launch receipt = %#v, %v", receipt, err)
	}
	want := []string{
		"--disk", opts.DiskPath, "--kernel", opts.KernelPath,
		"--cpus", "2", "--mem", "512", "--virtiofs", "devd:/a control directory",
		"--env", "PATH=/bin", "--workdir", "/", "--", "/bin/sh", "-c", "echo guest",
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(opts.LogFile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(string(data), "echo guest\n") {
			got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments = %#v, want %#v", got, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("companion did not capture arguments: %s", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := Stop(StopOpts{Process: process}); err != nil {
		t.Fatal(err)
	}
	if running, err := process.Running(); err != nil || running {
		t.Fatalf("process remains after stop: %v, %v", running, err)
	}
}

func TestStopDoesNotSignalRecycledPID(t *testing.T) {
	opts := fakeRuntime(t)
	process, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Stop(StopOpts{Process: process}) }()
	stale := process
	stale.StartTime = "a different process incarnation"
	if err := Stop(StopOpts{Process: stale}); err != nil {
		t.Fatal(err)
	}
	if running, err := process.Running(); err != nil || !running {
		t.Fatalf("stale receipt stopped an unrelated process: %v, %v", running, err)
	}
}

func TestStartRejectsInvalidConfigBeforeLaunch(t *testing.T) {
	opts := fakeRuntime(t)
	for _, mutate := range []func(*StartOpts){
		func(o *StartOpts) { o.ProcessFile = "" },
		func(o *StartOpts) { o.CPUs = 256 },
		func(o *StartOpts) { o.Memory = -1 },
		func(o *StartOpts) { o.Env = make([]string, 65) },
		func(o *StartOpts) { o.Mounts = make([]Mount, 9) },
	} {
		invalid := opts
		mutate(&invalid)
		if _, err := Start(invalid); err == nil {
			t.Fatal("invalid configuration launched")
		}
	}
	if _, err := os.Stat(opts.LogFile); !os.IsNotExist(err) {
		t.Fatal("invalid configuration reached launch side effects")
	}
}

func TestReadProcessRejectsCorruptReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "process.json")
	if process, err := ReadProcess(path); err != nil || process != nil {
		t.Fatalf("missing receipt = %v, %v", process, err)
	}
	for _, data := range []string{"{", `{}`, `{"pid":123}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProcess(path); err == nil {
			t.Fatalf("accepted invalid receipt %s", data)
		}
	}
}
