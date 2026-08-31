package cli

import (
	"fmt"
	"os"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/storage"
	"devd/internal/vm"
)

type forkOverrides struct {
	CPUs               int
	CPUsChanged        bool
	Memory             int
	MemoryChanged      bool
	Ports              []int
	PortsChanged       bool
	Mount              string
	MountChanged       bool
	UserCommand        string
	UserCommandChanged bool
}

// doFork creates a stopped destination record and disk. The run command starts
// it immediately after this function succeeds.
func doFork(sourceName, destinationName string, overrides forkOverrides) (*db.Workspace, error) {
	if err := config.ValidateWorkspaceName(sourceName); err != nil {
		return nil, err
	}
	if err := config.ValidateWorkspaceName(destinationName); err != nil {
		return nil, err
	}
	if sourceName == destinationName {
		return nil, fmt.Errorf("fork destination must differ from source")
	}
	if err := vm.CheckRuntime(); err != nil {
		return nil, err
	}

	database, err := db.Open()
	if err != nil {
		return nil, err
	}
	defer database.Close()
	source, err := db.GetWorkspace(database, sourceName)
	if err != nil {
		return nil, err
	}
	if source.State == "running" && vm.IsRunning(source.PID) {
		return nil, fmt.Errorf("workspace %q is running; stop it before forking", sourceName)
	}
	if source.State == "running" {
		if err := db.SetWorkspaceState(database, sourceName, "stopped", 0); err != nil {
			return nil, fmt.Errorf("reconcile source state: %w", err)
		}
	}
	if source.DiskPath == "" {
		return nil, fmt.Errorf("workspace %q has no ext4 disk path", sourceName)
	}
	if info, err := os.Stat(source.DiskPath); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return nil, fmt.Errorf("source disk %s: %w", source.DiskPath, err)
	}
	exists, err := db.WorkspaceExists(database, destinationName)
	if err != nil {
		return nil, fmt.Errorf("check destination name: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("workspace %q already exists", destinationName)
	}

	sourceCfg, err := storage.ReadWorkspaceConfig(source.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	mountHost, mountGuest := sourceCfg.MountHost, sourceCfg.MountGuest
	if overrides.MountChanged {
		mountHost, mountGuest, err = parseMount(overrides.Mount)
		if err != nil {
			return nil, err
		}
	} else if mountHost != "" {
		if info, statErr := os.Stat(mountHost); statErr != nil || !info.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("not a directory")
			}
			return nil, fmt.Errorf("source host mount %s: %w (override with --mount)", mountHost, statErr)
		}
	}

	cpus, memory := source.CPUs, source.Memory
	if overrides.CPUsChanged {
		cpus = overrides.CPUs
	}
	if overrides.MemoryChanged {
		memory = overrides.Memory
	}
	if cpus <= 0 || cpus > 255 || memory <= 0 {
		return nil, fmt.Errorf("fork CPU and memory overrides must be positive (cpus <= 255)")
	}
	userCommand := sourceCfg.UserCommand
	if overrides.UserCommandChanged {
		userCommand = overrides.UserCommand
	}
	reservedPorts, err := db.GetReservedPorts(database, sourceName)
	if err != nil {
		return nil, fmt.Errorf("read source ports: %w", err)
	}
	if overrides.PortsChanged {
		reservedPorts = overrides.Ports
	}

	sshPort, err := db.NextSSHPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate ssh port: %w", err)
	}
	relayPort, err := db.NextRelayPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate relay port: %w", err)
	}
	wsDir, err := config.WorkspaceDir(destinationName)
	if err != nil {
		return nil, err
	}
	diskPath, err := config.WorkspaceDiskPath(destinationName)
	if err != nil {
		return nil, err
	}
	createdRecord := false
	success := false
	defer func() {
		if success {
			return
		}
		if createdRecord {
			_ = db.DeleteWorkspace(database, destinationName)
		}
		_ = os.RemoveAll(wsDir)
	}()

	started := time.Now()
	if err := storage.CloneDisk(source.DiskPath, diskPath); err != nil {
		return nil, err
	}
	destinationCfg := *sourceCfg
	destinationCfg.MountHost = mountHost
	destinationCfg.MountGuest = mountGuest
	destinationCfg.UserCommand = userCommand
	destinationCfg.ParentName = sourceName
	if err := storage.WriteWorkspaceConfig(wsDir, destinationCfg); err != nil {
		return nil, err
	}
	if err := vm.WriteWorkspaceFiles(wsDir, vm.WorkspaceFilesOpts{
		UserCommand:  userCommand,
		ImageWorkdir: destinationCfg.WorkingDir,
		MountGuest:   mountGuest,
	}); err != nil {
		return nil, err
	}
	if err := vm.MarkRegenerateIdentity(wsDir); err != nil {
		return nil, err
	}

	destination := &db.Workspace{
		Name:         destinationName,
		Image:        source.Image,
		ImageDigest:  source.ImageDigest,
		WorkspaceDir: wsDir,
		DiskPath:     diskPath,
		ParentName:   sourceName,
		SSHPort:      sshPort,
		RelayPort:    relayPort,
		CPUs:         cpus,
		Memory:       memory,
		State:        "stopped",
	}
	if err := db.CreateWorkspace(database, destination); err != nil {
		return nil, fmt.Errorf("record fork: %w", err)
	}
	createdRecord = true
	for _, port := range reservedPorts {
		if err := db.AddReservedPort(database, destinationName, port); err != nil {
			return nil, fmt.Errorf("copy reserved port %d: %w", port, err)
		}
	}
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}

	success = true
	fmt.Printf("INFO Forked %q → %q in %s\n", sourceName, destinationName, time.Since(started).Round(time.Millisecond))
	fmt.Printf("     Disk: %s\n", diskPath)
	if mountHost == "" {
		fmt.Printf("     Host mount: none\n")
	} else {
		fmt.Printf("     Host mount: %s → %s (host files were not copied)\n", mountHost, mountGuest)
	}
	return destination, nil
}
