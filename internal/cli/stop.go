package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/workspace"
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
	unlock, err := workspace.Lock(name)
	if err != nil {
		return err
	}
	defer unlock()

	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	ws, err := loadWorkspace(database, name)
	if err != nil {
		return err
	}

	if ws.State != "running" {
		fmt.Printf("INFO Workspace %q is already stopped\n", name)
		return nil
	}

	fmt.Printf("INFO Stopping workspace %q (PID %d)...\n", name, ws.PID)

	if err := stopWorkspace(database, ws); err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}

	fmt.Printf("INFO Workspace %q stopped\n", name)
	return nil
}
