package cli

import (
	"fmt"
	"os"
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
}

func provisionWorkspace(opts provisionOptions) (*db.Workspace, error) {
	if err := config.ValidateWorkspaceName(opts.Name); err != nil {
		return nil, err
	}
	if opts.CPUs <= 0 || opts.CPUs > 255 {
		return nil, fmt.Errorf("cpus must be between 1 and 255")
	}
	if opts.Memory <= 0 {
		return nil, fmt.Errorf("memory must be greater than zero")
	}
	if err := validatePorts(opts.Ports); err != nil {
		return nil, err
	}
	if err := vm.CheckRuntime(); err != nil {
		return nil, err
	}
	if err := storage.CheckDependencies(); err != nil {
		return nil, err
	}

	mountHost, mountGuest, err := parseMount(opts.Mount)
	if err != nil {
		return nil, err
	}

	database, err := db.Open()
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	exists, err := db.WorkspaceExists(database, opts.Name)
	if err != nil {
		return nil, fmt.Errorf("check workspace name: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("workspace %q already exists", opts.Name)
	}

	sshPort, err := nextAvailableSSHPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate ssh port: %w", err)
	}
	relayPort, err := db.NextRelayPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate relay port: %w", err)
	}
	if _, err := ssh.EnsureKeypair(); err != nil {
		return nil, fmt.Errorf("ssh keypair: %w", err)
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

	wsDir, err := config.WorkspaceDir(opts.Name)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}
	diskPath, err := config.WorkspaceDiskPath(opts.Name)
	if err != nil {
		return nil, fmt.Errorf("workspace disk path: %w", err)
	}
	createdRecord := false
	success := false
	defer func() {
		if success {
			return
		}
		if createdRecord {
			_ = db.DeleteWorkspace(database, opts.Name)
		}
		_ = os.RemoveAll(wsDir)
	}()

	cloneStart := time.Now()
	if err := storage.CloneDisk(template.DiskPath, diskPath); err != nil {
		return nil, err
	}
	fmt.Printf("INFO Cloned workspace disk in %s\n", time.Since(cloneStart).Round(time.Millisecond))

	workspaceSpec := workspace.Spec{
		Image:       template.Manifest.Image,
		ImageDigest: template.Manifest.Digest,
		Environment: template.Manifest.Environment,
		WorkingDir:  template.Manifest.WorkingDir,
		UserCommand: opts.UserCommand,
		MountHost:   mountHost,
		MountGuest:  mountGuest,
	}
	if err := workspace.Save(wsDir, workspaceSpec); err != nil {
		return nil, err
	}

	ws := &db.Workspace{
		Name:         opts.Name,
		Image:        template.Manifest.Image,
		ImageDigest:  template.Manifest.Digest,
		WorkspaceDir: wsDir,
		DiskPath:     diskPath,
		SSHPort:      sshPort,
		RelayPort:    relayPort,
		CPUs:         opts.CPUs,
		Memory:       opts.Memory,
		State:        "stopped",
	}
	if err := db.CreateWorkspace(database, ws); err != nil {
		return nil, fmt.Errorf("record workspace: %w", err)
	}
	createdRecord = true
	for _, port := range opts.Ports {
		if err := db.AddReservedPort(database, opts.Name, port); err != nil {
			return nil, fmt.Errorf("reserve port %d: %w", port, err)
		}
	}
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}

	success = true
	return ws, nil
}
