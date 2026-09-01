package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/vm"
)

var rmCmd = &cobra.Command{
	Use:   "rm <workspace>",
	Short: "Remove a workspace and its ext4 disk",
	Long:  `Remove a workspace and its writable disk clone. If running, use -f to stop it first. Immutable image templates are retained for future creates.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runRm,
}

var flagForce bool

func init() {
	rmCmd.Flags().BoolVarP(&flagForce, "force", "f", false, "force remove (stop if running)")
}

func runRm(cmd *cobra.Command, args []string) error {
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
	if ws.State == "running" && !vm.IsRunning(ws.PID) {
		if err := db.SetWorkspaceState(database, name, "stopped", 0); err != nil {
			return fmt.Errorf("reconcile workspace state: %w", err)
		}
		ws.State = "stopped"
		ws.PID = 0
	}
	if ws.State == "running" {
		if !flagForce {
			return fmt.Errorf("workspace %q is running — use -f to force remove", name)
		}
		fmt.Printf("INFO Stopping workspace %q...\n", name)
		if err := stopWorkspace(ws); err != nil {
			fmt.Printf("WARN stop VM: %v\n", err)
		}
	}

	fmt.Printf("INFO Removing workspace %q...\n", name)
	if ws.WorkspaceDir != "" {
		if err := os.RemoveAll(ws.WorkspaceDir); err != nil {
			return fmt.Errorf("remove workspace files: %w", err)
		}
	}
	if err := db.DeleteWorkspace(database, name); err != nil {
		return fmt.Errorf("delete from db: %w", err)
	}
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}
	if ports, portsErr := db.GetAllReservedPorts(database); portsErr == nil && len(ports) == 0 {
		shutdownProxyDaemon()
	}
	fmt.Printf("INFO Workspace %q removed\n", name)
	return nil
}
