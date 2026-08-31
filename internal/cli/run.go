package cli

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/vm"
)

var runCmd = &cobra.Command{
	Use:   "run [image]",
	Short: "Create and start a new workspace VM",
	Long: `Create and start a new microVM workspace. Like 'ignite run' — one command
to go from image to running VM with SSH access.

Equivalent to 'devd create' + 'devd start'.

NOTE: If multiple workspaces share ports, start 'devd daemon' BEFORE
running the second workspace so it can pre-empt the contested ports.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRun,
}

var (
	flagName   string
	flagCPUs   int
	flagMemory int
	flagPorts  []int
	flagMount  string
	flagCmd    string
)

func init() {
	runCmd.Flags().StringVar(&flagName, "name", "", "workspace name (required)")
	runCmd.Flags().IntVar(&flagCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	runCmd.Flags().IntVar(&flagMemory, "memory", config.DefaultMemory, "memory in MB")
	runCmd.Flags().IntSliceVar(&flagPorts, "ports", nil, "ports to reserve (contested ports get proxied)")
	runCmd.Flags().StringVar(&flagMount, "mount", "", "host:guest volume mount (e.g. .:/workspace)")
	runCmd.Flags().StringVar(&flagCmd, "cmd", "", "command to run inside VM after boot")
	runCmd.MarkFlagRequired("name")
}

func runRun(cmd *cobra.Command, args []string) error {
	totalStart := time.Now()

	image := config.DefaultImage
	if len(args) > 0 {
		image = args[0]
	}

	if dc, _ := config.LoadDevContainer("."); dc != nil {
		if len(args) == 0 && dc.Image != "" {
			image = dc.Image
		}
		if len(flagPorts) == 0 && len(dc.ForwardPorts) > 0 {
			flagPorts = dc.ForwardPorts
		}
		if flagCmd == "" && dc.PostCreateCommand != "" {
			flagCmd = dc.PostCreateCommand
		}
		if flagMount == "" {
			flagMount = ".:/workspace"
		}
	}

	// Phase 1: create
	ws, err := doCreate(flagName, image, flagCPUs, flagMemory, flagPorts, flagMount, flagCmd)
	if err != nil {
		return err
	}

	// Phase 2: start
	logFile := filepath.Join(ws.RootfsDir, "vm.log")
	initPath := fmt.Sprintf("/devd/workspaces/%s/init.sh", flagName)

	database, err := db.Open()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	fmt.Println("INFO Starting VM...")
	bootStart := time.Now()

	if err := ensurePortAvailable(ws.SSHPort); err != nil {
		return fmt.Errorf("start workspace %q: %w", flagName, err)
	}

	pid, err := vm.Start(vm.StartOpts{
		Name:    flagName,
		Command: "/bin/sh",
		Args:    []string{initPath},
		LogFile: logFile,
	})
	if err != nil {
		if delErr := db.DeleteWorkspace(database, flagName); delErr != nil {
			fmt.Printf("WARN cleanup db: %v\n", delErr)
		}
		if delErr := vm.Delete(flagName); delErr != nil {
			fmt.Printf("WARN cleanup vm: %v\n", delErr)
		}
		return fmt.Errorf("start VM: %w", err)
	}

	if err := db.SetWorkspaceState(database, flagName, "running", pid); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop vm after db failure: %v\n", stopErr)
		}
		return fmt.Errorf("update state: %w", err)
	}
	if err := db.SetActiveWorkspace(database, flagName); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop vm after db failure: %v\n", stopErr)
		}
		return fmt.Errorf("set active: %w", err)
	}

	fmt.Printf("INFO Waiting for SSH on port %d...\n", ws.SSHPort)
	if err := waitForSSH(ws.SSHPort, pid, 30*time.Second); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop VM after startup failure: %v\n", stopErr)
		} else if stateErr := db.SetWorkspaceState(database, flagName, "stopped", 0); stateErr != nil {
			fmt.Printf("WARN update state after startup failure: %v\n", stateErr)
		}
		return fmt.Errorf("workspace %q failed to start: %w (check log: %s)", flagName, err, logFile)
	}

	bootElapsed := time.Since(bootStart)
	totalElapsed := time.Since(totalStart)
	fmt.Printf("INFO SSH ready\n")

	fmt.Println()
	fmt.Printf("     Name:    %s\n", flagName)
	fmt.Printf("     Image:   %s\n", image)
	fmt.Printf("     SSH:     ssh devd-%s  (or: devd ssh %s)\n", flagName, flagName)
	fmt.Printf("     Port:    %d\n", ws.SSHPort)
	fmt.Printf("     PID:     %d\n", pid)
	fmt.Printf("     Log:     %s\n", logFile)
	fmt.Printf("     Create:  %.2fs\n", totalElapsed.Seconds()-bootElapsed.Seconds())
	fmt.Printf("     Boot:    %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     Total:   %.2fs\n", totalElapsed.Seconds())

	return nil
}

// ensurePortAvailable catches stale VMs or other processes before booting a guest
// whose sshd would then fail permanently with EADDRINUSE.
func ensurePortAvailable(port int) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("SSH port %d is already in use: %w", port, err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release SSH port %d after availability check: %w", port, err)
	}
	return nil
}

// waitForSSH polls the SSH port until a connection succeeds, the VM exits, or timeout.
func waitForSSH(port, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	for time.Now().Before(deadline) {
		if !vm.IsRunning(pid) {
			return fmt.Errorf("VM process exited before SSH became ready")
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("SSH not ready on port %d after %s", port, timeout)
}
