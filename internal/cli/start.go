package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/workspace"
)

var startCmd = &cobra.Command{
	Use:   "start <workspace>",
	Short: "Start a stopped workspace",
	Long:  `Boot a previously created workspace and wait until SSH is ready.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
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
	bootElapsed, err := startWorkspace(database, ws)
	if err != nil {
		return err
	}
	fmt.Printf("INFO SSH ready in %.2fs\n", bootElapsed.Seconds())
	fmt.Printf("     PID: %d\n", ws.PID)
	return nil
}
