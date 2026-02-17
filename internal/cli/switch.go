package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"devd/internal/db"
)

var switchCmd = &cobra.Command{
	Use:   "switch <workspace>",
	Short: "Switch the active workspace",
	Long: `Switch which workspace receives traffic on contested ports.
The proxy daemon (devd daemon) routes new connections to the active workspace.
Both VMs keep running — no processes are killed or restarted.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwitch,
}

func runSwitch(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("workspace %q is not running (state: %s)", name, ws.State)
	}

	// Get current active for reporting
	current, _ := db.GetActiveWorkspace(database)
	if current != nil && current.Name == name {
		fmt.Printf("INFO Workspace %q is already active\n", name)
		return nil
	}

	if err := db.SetActiveWorkspace(database, name); err != nil {
		return fmt.Errorf("set active: %w", err)
	}

	if current != nil {
		fmt.Printf("INFO Switched active workspace: %s → %s\n", current.Name, name)
	} else {
		fmt.Printf("INFO Switched active workspace to %q\n", name)
	}

	// Report contested ports
	contested, _ := db.GetContestedPorts(database)
	if len(contested) > 0 {
		fmt.Printf("INFO Contested ports %v now route to %q\n", contested, name)
		fmt.Println("INFO (ensure 'devd daemon' is running for proxy to take effect)")
	}

	return nil
}
