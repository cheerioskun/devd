package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOperationLocks(t *testing.T) {
	if os.Getenv("DEVD_TEST_LOCK_CHILD") == "1" {
		if unlock, err := Lock("source"); err == nil {
			unlock()
			t.Fatal("child acquired parent's lock")
		}
		return
	}
	t.Setenv("DEVD_DIR", t.TempDir())
	unlock, err := Lock("source", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Failure to acquire the second name must release the first.
	if release, err := Lock("destination", "source"); err == nil {
		release()
		t.Fatal("overlapping operation was allowed")
	}
	release, err := Lock("destination")
	if err != nil {
		t.Fatalf("failed acquisition leaked a lock: %v", err)
	}
	release()
	child := exec.Command(os.Args[0], "-test.run=^TestOperationLocks$")
	child.Env = append(os.Environ(), "DEVD_TEST_LOCK_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process exclusion: %v\n%s", err, output)
	}
	unlock()
	// Removing workspace files must not replace the lock inode.
	path := filepath.Join(os.Getenv("DEVD_DIR"), "locks", "source.lock")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err = Lock("source")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock inode was replaced")
	}
}

func TestLockRejectsUnsafeNames(t *testing.T) {
	t.Setenv("DEVD_DIR", t.TempDir())
	if unlock, err := Lock("../escape"); err == nil {
		unlock()
		t.Fatal("unsafe name accepted")
	}
}
