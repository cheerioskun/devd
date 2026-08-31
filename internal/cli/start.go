package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/vm"
)

var startCmd = &cobra.Command{
	Use:   "start <workspace>",
	Short: "Start a stopped workspace VM",
	Long: `Boot a previously created workspace. Like 'ignite start'.

For contested ports to work, ensure 'devd daemon' is running BEFORE
starting VMs — it must pre-empt ports before TSI can grab them.`,
	Args: cobra.ExactArgs(1),
	RunE: runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]

	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	ws, err := db.GetWorkspace(database, name)
	if err != nil {
		return err
	}
	if ws.State == "running" {
		return fmt.Errorf("workspace %q is already running (PID %d)", name, ws.PID)
	}

	logFile := filepath.Join(ws.RootfsDir, "vm.log")
	initPath := fmt.Sprintf("/devd/workspaces/%s/init.sh", name)

	fmt.Printf("INFO Starting workspace %q...\n", name)
	bootStart := time.Now()

	if err := ensurePortAvailable(ws.SSHPort); err != nil {
		return fmt.Errorf("start workspace %q: %w", name, err)
	}

	pid, err := vm.Start(vm.StartOpts{
		Name:    name,
		Command: "/bin/sh",
		Args:    []string{initPath},
		LogFile: logFile,
	})
	if err != nil {
		return fmt.Errorf("start VM: %w", err)
	}

	if err := db.SetWorkspaceState(database, name, "running", pid); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop vm after db failure: %v\n", stopErr)
		}
		return fmt.Errorf("update state: %w", err)
	}
	if err := db.SetActiveWorkspace(database, name); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop vm after db failure: %v\n", stopErr)
		}
		return fmt.Errorf("set active: %w", err)
	}

	fmt.Printf("INFO Waiting for SSH on port %d...\n", ws.SSHPort)
	if err := waitForSSH(ws.SSHPort, pid, 30*time.Second); err != nil {
		if stopErr := vm.Stop(pid); stopErr != nil {
			fmt.Printf("WARN stop VM after startup failure: %v\n", stopErr)
		} else if stateErr := db.SetWorkspaceState(database, name, "stopped", 0); stateErr != nil {
			fmt.Printf("WARN update state after startup failure: %v\n", stateErr)
		}
		return fmt.Errorf("workspace %q failed to start: %w (check log: %s)", name, err, logFile)
	}
	bootElapsed := time.Since(bootStart)

	fmt.Printf("INFO SSH ready in %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     PID: %d\n", pid)
	return nil
}
