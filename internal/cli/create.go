package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/storage"
	"devd/internal/vm"
)

var createCmd = &cobra.Command{
	Use:    "create [image]",
	Short:  "Create a stopped workspace",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE:   runCreate,
}

var (
	createName    string
	createCPUs    int
	createMemory  int
	createPorts   []int
	createMount   string
	createUserCmd string
)

func init() {
	createCmd.Flags().StringVar(&createName, "name", "", "workspace name (required)")
	createCmd.Flags().IntVar(&createCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	createCmd.Flags().IntVar(&createMemory, "memory", config.DefaultMemory, "memory in MB")
	createCmd.Flags().IntSliceVar(&createPorts, "ports", nil, "ports to reserve (contested ports get proxied)")
	createCmd.Flags().StringVar(&createMount, "mount", "", "host:guest volume mount (e.g. .:/workspace)")
	createCmd.Flags().StringVar(&createUserCmd, "cmd", "", "command to run inside VM after boot")
	_ = createCmd.MarkFlagRequired("name")
}

// doCreate handles create-only logic shared by create and run.
func doCreate(name, image string, cpus, memory int, ports []int, mount, userCmd string) (*db.Workspace, error) {
	if err := config.ValidateWorkspaceName(name); err != nil {
		return nil, err
	}
	if cpus <= 0 || cpus > 255 {
		return nil, fmt.Errorf("cpus must be between 1 and 255")
	}
	if memory <= 0 {
		return nil, fmt.Errorf("memory must be greater than zero")
	}
	if err := validatePorts(ports); err != nil {
		return nil, err
	}
	if err := vm.CheckRuntime(); err != nil {
		return nil, err
	}
	if err := storage.CheckDependencies(); err != nil {
		return nil, err
	}

	mountHost, mountGuest, err := parseMount(mount)
	if err != nil {
		return nil, err
	}

	database, err := db.Open()
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	exists, err := db.WorkspaceExists(database, name)
	if err != nil {
		return nil, fmt.Errorf("check workspace name: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("workspace %q already exists", name)
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

	fmt.Printf("INFO Preparing image %q...\n", storage.QualifyImage(image))
	prepareStart := time.Now()
	template, err := storage.EnsureTemplate(image)
	if err != nil {
		return nil, fmt.Errorf("prepare image: %w", err)
	}
	if template.Cached {
		fmt.Printf("INFO Using cached ext4 template %s\n", template.Manifest.Digest)
	} else {
		fmt.Printf("INFO Prepared ext4 template in %.2fs\n", time.Since(prepareStart).Seconds())
	}

	wsDir, err := config.WorkspaceDir(name)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}
	diskPath, err := config.WorkspaceDiskPath(name)
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
			_ = db.DeleteWorkspace(database, name)
		}
		_ = os.RemoveAll(wsDir)
	}()

	cloneStart := time.Now()
	if err := storage.CloneDisk(template.DiskPath, diskPath); err != nil {
		return nil, err
	}
	fmt.Printf("INFO Cloned workspace disk in %s\n", time.Since(cloneStart).Round(time.Millisecond))

	workspaceCfg := storage.WorkspaceConfig{
		Image:       template.Manifest.Image,
		ImageDigest: template.Manifest.Digest,
		Environment: template.Manifest.Environment,
		WorkingDir:  template.Manifest.WorkingDir,
		UserCommand: userCmd,
		MountHost:   mountHost,
		MountGuest:  mountGuest,
	}
	if err := storage.WriteWorkspaceConfig(wsDir, workspaceCfg); err != nil {
		return nil, fmt.Errorf("write workspace config: %w", err)
	}
	if err := vm.WriteWorkspaceFiles(wsDir, vm.WorkspaceFilesOpts{
		UserCommand:  userCmd,
		ImageWorkdir: template.Manifest.WorkingDir,
		MountGuest:   mountGuest,
	}); err != nil {
		return nil, err
	}

	ws := &db.Workspace{
		Name:         name,
		Image:        template.Manifest.Image,
		ImageDigest:  template.Manifest.Digest,
		WorkspaceDir: wsDir,
		DiskPath:     diskPath,
		SSHPort:      sshPort,
		RelayPort:    relayPort,
		CPUs:         cpus,
		Memory:       memory,
		State:        "stopped",
	}
	if err := db.CreateWorkspace(database, ws); err != nil {
		return nil, fmt.Errorf("record workspace: %w", err)
	}
	createdRecord = true
	for _, port := range ports {
		if err := db.AddReservedPort(database, name, port); err != nil {
			return nil, fmt.Errorf("reserve port %d: %w", port, err)
		}
	}
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}

	success = true
	return ws, nil
}

func runCreate(cmd *cobra.Command, args []string) error {
	image := config.DefaultImage
	if len(args) > 0 {
		image = args[0]
	}

	dc, err := config.LoadDevContainer(".")
	if err != nil {
		return err
	}
	if dc != nil {
		if len(args) == 0 && dc.Image != "" {
			image = dc.Image
		}
		if len(createPorts) == 0 && len(dc.ForwardPorts) > 0 {
			createPorts = dc.ForwardPorts
		}
		if dc.PostCreateCommand != "" {
			fmt.Println("WARN devcontainer postCreateCommand is not supported yet")
		}
		if createMount == "" {
			createMount = ".:/workspace"
		}
	}

	ws, err := doCreate(createName, image, createCPUs, createMemory, createPorts, createMount, createUserCmd)
	if err != nil {
		return err
	}
	fmt.Printf("INFO Workspace %q created (stopped)\n", ws.Name)
	fmt.Printf("     Start: devd start %s\n", ws.Name)
	return nil
}
