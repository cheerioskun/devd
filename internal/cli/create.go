package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/vm"
)

var createCmd = &cobra.Command{
	Use:   "create [image]",
	Short: "Create a workspace without starting it",
	Long: `Create a new microVM workspace definition. Like 'ignite create'.
The VM is registered but not started — use 'devd start' to boot it.

This is useful when you need the daemon to pre-empt contested ports
before any VMs start.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCreate,
}

var (
	createName   string
	createCPUs   int
	createMemory int
	createPorts  []int
	createMount  string
	createUserCmd string
)

func init() {
	createCmd.Flags().StringVar(&createName, "name", "", "workspace name (required)")
	createCmd.Flags().IntVar(&createCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	createCmd.Flags().IntVar(&createMemory, "memory", config.DefaultMemory, "memory in MB")
	createCmd.Flags().IntSliceVar(&createPorts, "ports", nil, "ports to reserve (contested ports get proxied)")
	createCmd.Flags().StringVar(&createMount, "mount", "", "host:guest volume mount (e.g. .:/workspace)")
	createCmd.Flags().StringVar(&createUserCmd, "cmd", "", "command to run inside VM after boot")
	createCmd.MarkFlagRequired("name")
}

// doCreate handles the create-only logic. Returns the workspace record.
func doCreate(name, image string, cpus, memory int, ports []int, mount, userCmd string) (*db.Workspace, error) {
	if err := vm.CheckKrunvm(); err != nil {
		return nil, err
	}

	database, err := db.Open()
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if existing, _ := db.GetWorkspace(database, name); existing != nil {
		return nil, fmt.Errorf("workspace %q already exists (state: %s)", name, existing.State)
	}

	sshPort, err := db.NextSSHPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate ssh port: %w", err)
	}
	relayPort, err := db.NextRelayPort(database)
	if err != nil {
		return nil, fmt.Errorf("allocate relay port: %w", err)
	}

	pubKey, err := ssh.EnsureKeypair()
	if err != nil {
		return nil, fmt.Errorf("ssh keypair: %w", err)
	}

	wsDir, err := config.WorkspaceDir(name)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}

	_, err = vm.WriteInitScript(wsDir, vm.GuestInitOpts{
		Label:     name,
		SSHPort:   sshPort,
		PublicKey: strings.TrimSpace(pubKey),
		UserCmd:   userCmd,
	})
	if err != nil {
		return nil, fmt.Errorf("write init script: %w", err)
	}

	devdDir, err := config.DevdDir()
	if err != nil {
		return nil, fmt.Errorf("devd dir: %w", err)
	}
	volumes := map[string]string{
		devdDir: "/devd",
	}
	if mount != "" {
		parts := strings.SplitN(mount, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("--mount must be host:guest (e.g. .:/workspace)")
		}
		hostPath := parts[0]
		if !filepath.IsAbs(hostPath) {
			cwd, _ := os.Getwd()
			hostPath = filepath.Join(cwd, hostPath)
		}
		volumes[hostPath] = parts[1]
	}

	fmt.Printf("INFO Creating workspace %q (%s, %d CPUs, %d MB)\n", name, image, cpus, memory)
	createStart := time.Now()

	if _, err := vm.Create(vm.CreateOpts{
		Name:    name,
		Image:   image,
		CPUs:    cpus,
		Memory:  memory,
		Volumes: volumes,
	}); err != nil {
		return nil, fmt.Errorf("create VM: %w", err)
	}
	fmt.Printf("INFO Created in %.2fs\n", time.Since(createStart).Seconds())

	ws := &db.Workspace{
		Name:      name,
		Image:     image,
		RootfsDir: wsDir,
		SSHPort:   sshPort,
		RelayPort: relayPort,
		CPUs:      cpus,
		Memory:    memory,
		State:     "stopped",
		IsActive:  false,
	}
	if err := db.CreateWorkspace(database, ws); err != nil {
		vm.Delete(name)
		return nil, fmt.Errorf("record workspace: %w", err)
	}

	for _, p := range ports {
		if err := db.AddReservedPort(database, name, p); err != nil {
			return nil, fmt.Errorf("reserve port %d: %w", p, err)
		}
	}

	// Update SSH config
	allWs, _ := db.ListWorkspaces(database)
	var entries []ssh.SSHConfigEntry
	for _, w := range allWs {
		entries = append(entries, ssh.SSHConfigEntry{Name: w.Name, Port: w.SSHPort})
	}
	ssh.UpdateSSHConfig(entries)

	fmt.Printf("INFO Workspace %q created (stopped). Use 'devd start %s' to boot.\n", name, name)
	fmt.Printf("     SSH port: %d, Relay port: %d\n", sshPort, relayPort)

	return ws, nil
}

func runCreate(cmd *cobra.Command, args []string) error {
	image := config.DefaultImage
	if len(args) > 0 {
		image = args[0]
	}
	_, err := doCreate(createName, image, createCPUs, createMemory, createPorts, createMount, createUserCmd)
	return err
}
