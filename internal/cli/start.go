package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/vm"
)

var startCmd = &cobra.Command{
	Use:   "start <workspace>",
	Short: "Start a stopped ext4 workspace VM",
	Long: `Boot a previously created workspace.

For contested ports to work, ensure 'devd daemon' is running before starting
VMs so it can pre-empt those ports before TSI binds them.`,
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
	if ws.State == "running" && vm.IsRunning(ws.PID) {
		return fmt.Errorf("workspace %q is already running (PID %d)", name, ws.PID)
	}
	if ws.State == "running" {
		if err := db.SetWorkspaceState(database, name, "stopped", 0); err != nil {
			return fmt.Errorf("reconcile stale workspace state: %w", err)
		}
		ws.State = "stopped"
		ws.PID = 0
	}

	bootElapsed, err := startWorkspace(database, ws)
	if err != nil {
		return err
	}
	fmt.Printf("INFO SSH ready in %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     PID: %d\n", ws.PID)
	return nil
}
