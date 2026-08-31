package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"devd/internal/db"
)

var stopCmd = &cobra.Command{
	Use:   "stop <workspace>",
	Short: "Stop a running workspace VM",
	Long:  `Stop a workspace cleanly, with a bounded VMM-kill fallback.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runStop,
}

func runStop(cmd *cobra.Command, args []string) error {
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

	if ws.State != "running" {
		fmt.Printf("INFO Workspace %q is already stopped\n", name)
		return nil
	}

	fmt.Printf("INFO Stopping workspace %q (PID %d)...\n", name, ws.PID)

	if err := stopWorkspace(ws); err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}

	if err := db.SetWorkspaceState(database, name, "stopped", 0); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	fmt.Printf("INFO Workspace %q stopped\n", name)
	return nil
}
