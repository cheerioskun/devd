package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/vm"
)

var rmCmd = &cobra.Command{
	Use:   "rm <workspace>",
	Short: "Remove a workspace",
	Long: `Remove a workspace and its VM. If running, stop it first (use -f to force).
Like 'ignite rm'.`,
	Args: cobra.ExactArgs(1),
	RunE: runRm,
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

	if ws.State == "running" {
		if !flagForce {
			return fmt.Errorf("workspace %q is running — use -f to force remove", name)
		}
		fmt.Printf("INFO Stopping workspace %q...\n", name)
		if err := vm.Stop(ws.PID); err != nil {
			fmt.Printf("WARN stop vm: %v\n", err)
		}
	}

	fmt.Printf("INFO Removing workspace %q...\n", name)

	// Delete krunvm VM
	if err := vm.Delete(name); err != nil {
		fmt.Printf("WARN krunvm delete: %v\n", err)
	}

	// Remove workspace directory
	if ws.RootfsDir != "" {
		if err := os.RemoveAll(ws.RootfsDir); err != nil {
			fmt.Printf("WARN remove workspace dir: %v\n", err)
		}
	}

	// Remove from database
	if err := db.DeleteWorkspace(database, name); err != nil {
		return fmt.Errorf("delete from db: %w", err)
	}

	// Update SSH config
	allWs, _ := db.ListWorkspaces(database)
	var entries []ssh.SSHConfigEntry
	for _, w := range allWs {
		entries = append(entries, ssh.SSHConfigEntry{Name: w.Name, Port: w.SSHPort})
	}
	if err := ssh.UpdateSSHConfig(entries); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}

	fmt.Printf("INFO Workspace %q removed\n", name)
	return nil
}
