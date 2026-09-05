package cli

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/storage"
	"devd/internal/vm"
	"devd/internal/workspace"
)

var forkCmd = &cobra.Command{
	Use: "fork <source>", Short: "Clone and start a stopped workspace",
	Long: `Clone the complete disk state of a stopped workspace and start the new
workspace. The host project mount is reused unless --mount or --no-mount is
supplied.`,
	Args: cobra.ExactArgs(1), RunE: runFork,
}

var (
	forkName    string
	forkCPUs    int
	forkMemory  int
	forkPorts   []int
	forkMount   string
	forkNoMount bool
	forkUserCmd string
	forkKernel  string
)

func init() {
	forkCmd.Flags().StringVarP(&forkName, "name", "n", "", "new workspace name (generated when omitted)")
	forkCmd.Flags().IntVar(&forkCPUs, "cpus", config.DefaultCPUs, "number of vCPUs")
	forkCmd.Flags().IntVar(&forkMemory, "memory", config.DefaultMemory, "memory in MB")
	forkCmd.Flags().IntSliceVarP(&forkPorts, "ports", "p", nil, "host ports managed by devd")
	forkCmd.Flags().StringVar(&forkMount, "mount", "", "override host:guest volume mount")
	forkCmd.Flags().BoolVar(&forkNoMount, "no-mount", false, "do not reuse the source host mount")
	forkCmd.Flags().StringVar(&forkUserCmd, "cmd", "", "override startup command")
	forkCmd.Flags().StringVar(&forkKernel, "kernel", "", "override custom kernel (empty uses embedded kernel)")
}

func runFork(cmd *cobra.Command, args []string) error {
	if forkNoMount && cmd.Flags().Changed("mount") {
		return fmt.Errorf("--mount and --no-mount cannot be used together")
	}
	name := forkName
	var err error
	if name == "" {
		name, err = generatedWorkspaceName(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("INFO Generated workspace name %q\n", name)
	}
	unlock, err := workspace.Lock(args[0], name)
	if err != nil {
		return err
	}
	defer unlock()
	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()
	mount := forkMount
	if forkNoMount {
		mount = ""
	}
	started := time.Now()
	ws, err := doFork(database, args[0], name, forkOverrides{
		CPUs: forkCPUs, CPUsChanged: cmd.Flags().Changed("cpus"),
		Memory: forkMemory, MemoryChanged: cmd.Flags().Changed("memory"),
		Ports: forkPorts, PortsChanged: cmd.Flags().Changed("ports"),
		Mount: mount, MountChanged: cmd.Flags().Changed("mount") || forkNoMount,
		UserCommand: forkUserCmd, UserCommandChanged: cmd.Flags().Changed("cmd"),
		KernelPath: forkKernel, KernelPathChanged: cmd.Flags().Changed("kernel"),
	})
	if err != nil {
		return err
	}
	cloneElapsed := time.Since(started)
	bootElapsed, err := startWorkspace(database, ws)
	if err != nil {
		return err
	}
	fmt.Printf("INFO Workspace %q forked from %q and ready\n", ws.Name, args[0])
	fmt.Printf("     SSH:    devd ssh %s\n", ws.Name)
	fmt.Printf("     Clone:  %s\n", cloneElapsed.Round(time.Millisecond))
	fmt.Printf("     Boot:   %.2fs\n", bootElapsed.Seconds())
	return nil
}

// doFork resolves inheritance before invoking the shared publisher. The source
// disk lock spans cloning; the command holds both workspace operation locks.
func doFork(database *sql.DB, sourceName, destinationName string, overrides forkOverrides) (*db.Workspace, error) {
	if sourceName == destinationName {
		return nil, fmt.Errorf("fork destination must differ from source")
	}
	if _, err := vm.RuntimePath(); err != nil {
		return nil, err
	}
	source, err := loadWorkspace(database, sourceName)
	if err != nil {
		return nil, err
	}
	if source.State == "running" {
		return nil, fmt.Errorf("workspace %q is running; stop it before forking", sourceName)
	}
	diskLock, err := storage.LockDisk(source.DiskPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = diskLock.Close() }()
	spec, err := workspace.Load(source.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	ports, err := db.GetReservedPorts(database, sourceName)
	if err != nil {
		return nil, err
	}
	mount := ""
	if spec.MountHost != "" {
		mount = spec.MountHost + ":" + spec.MountGuest
	}
	plan := applyForkOverrides(workspacePlan{
		Image: source.Image, ImageDigest: source.ImageDigest,
		Spec: *spec, CPUs: source.CPUs, Memory: source.Memory, Ports: ports, Mount: mount,
	}, overrides)
	plan.ParentName = sourceName
	plan, err = resolvePlan(plan)
	if err != nil {
		return nil, err
	}
	return publishWorkspace(database, destinationName, source.DiskPath, plan)
}
