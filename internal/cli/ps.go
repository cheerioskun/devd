package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/vm"
)

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List workspaces",
	Long:  `List all workspaces and their current status. Like 'ignite ps'.`,
	RunE:  runPs,
}

var flagAll bool

func init() {
	psCmd.Flags().BoolVarP(&flagAll, "all", "a", false, "show all workspaces (including stopped)")
}

func runPs(cmd *cobra.Command, args []string) error {
	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	workspaces, err := db.ListWorkspaces(database)
	if err != nil {
		return err
	}

	// Reconcile state: check if PIDs are still alive
	for _, ws := range workspaces {
		if ws.State == "running" && !vm.IsRunning(ws.PID) {
			db.SetWorkspaceState(database, ws.Name, "stopped", 0)
			ws.State = "stopped"
			ws.PID = 0
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tIMAGE\tSTATE\tSSH PORT\tCPUS\tMEMORY\tACTIVE\tCREATED")

	for _, ws := range workspaces {
		if !flagAll && ws.State == "stopped" {
			continue
		}
		active := ""
		if ws.IsActive {
			active = "*"
		}
		age := formatAge(time.Since(ws.CreatedAt))
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d MB\t%s\t%s\n",
			ws.Name, ws.Image, ws.State, ws.SSHPort,
			ws.CPUs, ws.Memory, active, age,
		)
	}
	w.Flush()
	return nil
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
