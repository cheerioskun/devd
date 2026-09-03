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
	Short: "Create and start a new workspace",
	Long: `Create and start a new microVM workspace from an OCI image.

The positional argument is always an image. Use --name to choose the workspace
name; without it devd generates a name from the image. The current directory is
mounted at /workspace unless --mount or --no-mount is supplied.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRun,
}

var (
	flagName    string
	flagCPUs    int
	flagMemory  int
	flagPorts   []int
	flagMount   string
	flagNoMount bool
	flagCmd     string
	flagKernel  string
)

func init() {
	runCmd.Flags().StringVarP(&flagName, "name", "n", "", "workspace name (generated when omitted)")
	runCmd.Flags().IntVar(&flagCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	runCmd.Flags().IntVar(&flagMemory, "memory", config.DefaultMemory, "memory in MB")
	runCmd.Flags().IntSliceVarP(&flagPorts, "ports", "p", nil, "host ports managed by devd")
	runCmd.Flags().StringVar(&flagMount, "mount", "", "host:guest volume mount (default: .:/workspace)")
	runCmd.Flags().BoolVar(&flagNoMount, "no-mount", false, "do not mount the current directory")
	runCmd.Flags().StringVar(&flagCmd, "cmd", "", "startup command to run after each boot")
	runCmd.Flags().StringVar(&flagKernel, "kernel", "", "host path to a custom kernel")
}

func runRun(cmd *cobra.Command, args []string) error {
	totalStart := time.Now()
	image := config.DefaultImage
	if len(args) > 0 {
		image = args[0]
	}

	devContainer, err := config.LoadDevContainer(".")
	if err != nil {
		return err
	}
	if devContainer != nil {
		if len(args) == 0 && devContainer.Image != "" {
			image = devContainer.Image
		}
		if !cmd.Flags().Changed("ports") && len(devContainer.ForwardPorts) > 0 {
			flagPorts = devContainer.ForwardPorts
		}
		if devContainer.PostCreateCommand != "" {
			fmt.Println("WARN devcontainer postCreateCommand is not supported yet")
		}
	}

	if flagNoMount && cmd.Flags().Changed("mount") {
		return fmt.Errorf("--mount and --no-mount cannot be used together")
	}
	mount := flagMount
	if !flagNoMount && !cmd.Flags().Changed("mount") {
		mount = ".:/workspace"
	}

	name := flagName
	if name == "" {
		name, err = generatedWorkspaceName(image)
		if err != nil {
			return err
		}
		fmt.Printf("INFO Generated workspace name %q\n", name)
	}

	ws, err := provisionWorkspace(provisionOptions{
		Name:        name,
		Image:       image,
		CPUs:        flagCPUs,
		Memory:      flagMemory,
		Ports:       flagPorts,
		Mount:       mount,
		UserCommand: flagCmd,
		KernelPath:  flagKernel,
	})
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

	fmt.Printf("INFO Workspace %q ready\n\n", ws.Name)
	fmt.Printf("     Image:   %s\n", ws.Image)
	fmt.Printf("     Mount:   %s\n", mountSummary(mount))
	fmt.Printf("     SSH:     devd ssh %s  (or: ssh devd-%s)\n", ws.Name, ws.Name)
	fmt.Printf("     Log:     %s\n", logFile)
	fmt.Printf("     Create:  %.2fs\n", createElapsed.Seconds())
	fmt.Printf("     Boot:    %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     Total:   %.2fs\n", totalElapsed.Seconds())
	return nil
}

func mountSummary(mount string) string {
	if mount == "" {
		return "none"
	}
	return mount
}
