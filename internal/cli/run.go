package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
)

var runCmd = &cobra.Command{
	Use:   "run [image]",
	Short: "Create and start a new ext4 workspace VM",
	Long: `Create and start a new microVM workspace from an OCI image, or fork the
complete disk state of a stopped workspace with --fork.

Start the daemon first when multiple workspaces reserve the same contested port.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRun,
}

var (
	flagName       string
	flagCPUs       int
	flagMemory     int
	flagPorts      []int
	flagMount      string
	flagCmd        string
	flagForkSource string
)

func init() {
	runCmd.Flags().StringVar(&flagName, "name", "", "workspace name (required)")
	runCmd.Flags().IntVar(&flagCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	runCmd.Flags().IntVar(&flagMemory, "memory", config.DefaultMemory, "memory in MB")
	runCmd.Flags().IntSliceVar(&flagPorts, "ports", nil, "ports to reserve (contested ports get proxied)")
	runCmd.Flags().StringVar(&flagMount, "mount", "", "host:guest volume mount (e.g. .:/workspace)")
	runCmd.Flags().StringVar(&flagCmd, "cmd", "", "command to run inside VM after boot")
	runCmd.Flags().StringVar(&flagForkSource, "fork", "", "fork a stopped workspace instead of an OCI image")
	_ = runCmd.MarkFlagRequired("name")
}

func runRun(cmd *cobra.Command, args []string) error {
	totalStart := time.Now()
	var ws *db.Workspace
	var err error

	if flagForkSource != "" {
		if len(args) != 0 {
			return fmt.Errorf("an image argument cannot be used with --fork")
		}
		ws, err = doFork(flagForkSource, flagName, forkOverrides{
			CPUs:               flagCPUs,
			CPUsChanged:        cmd.Flags().Changed("cpus"),
			Memory:             flagMemory,
			MemoryChanged:      cmd.Flags().Changed("memory"),
			Ports:              flagPorts,
			PortsChanged:       cmd.Flags().Changed("ports"),
			Mount:              flagMount,
			MountChanged:       cmd.Flags().Changed("mount"),
			UserCommand:        flagCmd,
			UserCommandChanged: cmd.Flags().Changed("cmd"),
		})
	} else {
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
		ws, err = doCreate(flagName, image, flagCPUs, flagMemory, flagPorts, flagMount, flagCmd)
	}
	if err != nil {
		return err
	}
	createElapsed := time.Since(totalStart)

	database, err := db.Open()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	bootElapsed, err := startWorkspace(database, ws)
	if err != nil {
		return err
	}
	totalElapsed := time.Since(totalStart)
	logFile := filepath.Join(ws.WorkspaceDir, "vm.log")

	fmt.Printf("INFO SSH ready\n\n")
	fmt.Printf("     Name:    %s\n", flagName)
	fmt.Printf("     Image:   %s\n", ws.Image)
	fmt.Printf("     Disk:    %s\n", ws.DiskPath)
	fmt.Printf("     SSH:     ssh devd-%s  (or: devd ssh %s)\n", flagName, flagName)
	fmt.Printf("     Port:    %d\n", ws.SSHPort)
	fmt.Printf("     PID:     %d\n", ws.PID)
	fmt.Printf("     Log:     %s\n", logFile)
	fmt.Printf("     Create:  %.2fs\n", createElapsed.Seconds())
	fmt.Printf("     Boot:    %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     Total:   %.2fs\n", totalElapsed.Seconds())
	return nil
}
