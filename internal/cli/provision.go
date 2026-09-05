package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/storage"
	"devd/internal/vm"
	"devd/internal/workspace"
)

type provisionOptions struct {
	Name        string
	Image       string
	CPUs        int
	Memory      int
	Ports       []int
	Mount       string
	UserCommand string
	KernelPath  string
}

// provisionWorkspace resolves an image, then uses the same publisher as fork.
// The caller owns the workspace operation lock and command-scoped DB connection.
func provisionWorkspace(database *sql.DB, opts provisionOptions) (*db.Workspace, error) {
	if err := checkNewWorkspace(database, opts.Name); err != nil {
		return nil, err
	}
	plan, err := resolvePlan(workspacePlan{
		CPUs: opts.CPUs, Memory: opts.Memory, Ports: opts.Ports, Mount: opts.Mount,
		Spec: workspace.Spec{UserCommand: opts.UserCommand, KernelPath: opts.KernelPath},
	})
	if err != nil {
		return nil, err
	}
	if err := vm.CheckRuntime(); err != nil {
		return nil, err
	}
	fmt.Printf("INFO Preparing image %q...\n", storage.QualifyImage(opts.Image))
	prepareStart := time.Now()
	template, err := storage.EnsureTemplate(opts.Image)
	if err != nil {
		return nil, fmt.Errorf("prepare image: %w", err)
	}
	if template.Cached {
		fmt.Printf("INFO Using cached ext4 template %s\n", template.Manifest.Digest)
	} else {
		fmt.Printf("INFO Prepared ext4 template in %.2fs\n", time.Since(prepareStart).Seconds())
	}
	plan.Image = template.Manifest.Image
	plan.ImageDigest = template.Manifest.Digest
	plan.Spec.Environment = template.Manifest.Environment
	plan.Spec.WorkingDir = template.Manifest.WorkingDir
	return publishWorkspace(database, opts.Name, template.DiskPath, plan)
}

func checkNewWorkspace(database *sql.DB, name string) error {
	path, err := config.WorkspaceDir(name)
	if err != nil {
		return err
	}
	exists, err := db.WorkspaceExists(database, name)
	if err != nil {
		return fmt.Errorf("check workspace name: %w", err)
	}
	if exists {
		return fmt.Errorf("workspace %q already exists", name)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("workspace directory %s already exists without a record; preserve or remove it explicitly before reusing the name", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check workspace directory: %w", err)
	}
	return nil
}

// publishWorkspace owns staging until publication and never removes a directory
// it did not create. Disk cloning is outside the short metadata critical section.
// The caller holds the destination operation lock and, for fork, the source's
// operation and disk locks. A crash may leave recoverable orphan files, never a
// partially populated workspace visible through SQLite.
func publishWorkspace(database *sql.DB, name, sourceDisk string, plan workspacePlan) (*db.Workspace, error) {
	if err := checkNewWorkspace(database, name); err != nil {
		return nil, err
	}
	if _, err := imageEnvironment(plan.Spec.Environment); err != nil {
		return nil, err
	}
	parent, err := config.WorkspacesDir()
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".create-"+name+"-")
	if err != nil {
		return nil, fmt.Errorf("stage workspace: %w", err)
	}
	ownedDir := stage
	published := false
	defer func() {
		if !published {
			if err := os.RemoveAll(ownedDir); err != nil {
				fmt.Printf("WARN clean up workspace staging %s: %v\n", ownedDir, err)
			}
		}
	}()
	cloneStart := time.Now()
	if err := storage.CloneDisk(sourceDisk, filepath.Join(stage, "rootfs.ext4")); err != nil {
		return nil, err
	}
	if err := workspace.Save(stage, plan.Spec); err != nil {
		return nil, err
	}
	if plan.ParentName != "" {
		if err := workspace.MarkRegenerateIdentity(stage); err != nil {
			return nil, err
		}
	}

	unlock, err := db.LockMetadata()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := ssh.EnsureKeypair(); err != nil {
		return nil, err
	}
	sshPort, err := nextAvailableSSHPort(database)
	if err != nil {
		return nil, err
	}
	relayPort, err := db.NextRelayPort(database)
	if err != nil {
		return nil, err
	}
	if err := checkNewWorkspace(database, name); err != nil {
		return nil, err
	}
	destination := filepath.Join(parent, name)
	if err := os.Rename(stage, destination); err != nil {
		return nil, fmt.Errorf("publish workspace files: %w", err)
	}
	ownedDir = destination
	parentDir, err := os.Open(parent)
	if err != nil {
		return nil, err
	}
	err = parentDir.Sync()
	_ = parentDir.Close()
	if err != nil {
		return nil, fmt.Errorf("sync workspace publication: %w", err)
	}
	ws := &db.Workspace{
		Name: name, Image: plan.Image, ImageDigest: plan.ImageDigest,
		ParentName: plan.ParentName, WorkspaceDir: destination,
		DiskPath: filepath.Join(destination, "rootfs.ext4"),
		SSHPort:  sshPort, RelayPort: relayPort, CPUs: plan.CPUs, Memory: plan.Memory,
		State: "stopped",
	}
	if err := db.CreateWorkspace(database, ws, plan.Ports...); err != nil {
		return nil, fmt.Errorf("record workspace: %w", err)
	}
	published = true
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}
	fmt.Printf("INFO Cloned workspace disk in %s\n", time.Since(cloneStart).Round(time.Millisecond))
	return ws, nil
}
