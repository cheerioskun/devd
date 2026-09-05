package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/storage"
	"devd/internal/workspace"
)

var rmCmd = &cobra.Command{
	Use: "rm <workspace>", Short: "Remove a workspace and its ext4 disk",
	Long: `Remove a workspace and its writable disk clone. If running, use -f to stop it first. Immutable image templates are retained for future creates.`,
	Args: cobra.ExactArgs(1), RunE: runRm,
}

var flagForce bool

func init() {
	rmCmd.Flags().BoolVarP(&flagForce, "force", "f", false, "force remove (stop if running)")
}

func runRm(cmd *cobra.Command, args []string) error {
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
	if err := removeWorkspace(database, ws, flagForce); err != nil {
		return err
	}
	fmt.Printf("INFO Workspace %q removed\n", name)
	return nil
}

// removeWorkspace refuses to unlink a possibly live disk, including when a
// forced stop fails. Keep files recoverable until the metadata deletion commits.
func removeWorkspace(database *sql.DB, ws *db.Workspace, force bool) error {
	if ws.State == "running" {
		if !force {
			return fmt.Errorf("workspace %q is running — use -f to force remove", ws.Name)
		}
		if err := stopWorkspace(database, ws); err != nil {
			return err
		}
	}
	diskLock, err := storage.LockDisk(ws.DiskPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if diskLock != nil {
		defer func() { _ = diskLock.Close() }()
	}
	unlock, err := db.LockMetadata()
	if err != nil {
		return err
	}
	defer unlock()
	if err := db.DeleteWorkspace(database, ws.Name); err != nil {
		return fmt.Errorf("delete workspace metadata: %w", err)
	}
	// A crash here leaves an orphan, not a record referencing a destroyed disk.
	// checkNewWorkspace refuses to overwrite such files on a subsequent create.
	if err := os.RemoveAll(ws.WorkspaceDir); err != nil {
		return fmt.Errorf("workspace metadata removed; remove remaining files at %s: %w", ws.WorkspaceDir, err)
	}
	if err := updateSSHConfig(database); err != nil {
		fmt.Printf("WARN update ssh config: %v\n", err)
	}
	if ports, err := db.GetAllReservedPorts(database); err == nil && len(ports) == 0 {
		if err := shutdownProxyDaemon(); err != nil {
			fmt.Printf("WARN stop unused proxy: %v\n", err)
		}
	}
	return nil
}
