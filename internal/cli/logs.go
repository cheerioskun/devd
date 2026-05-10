package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"devd/internal/config"
)

var followLogs bool

var logsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "View logs for a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		wsDir, err := config.WorkspaceDir(name)
		if err != nil {
			return err
		}

		logFile := filepath.Join(wsDir, "vm.log")
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			return fmt.Errorf("no logs found for workspace %q", name)
		}

		if followLogs {
			// Shell out to tail for follow functionality
			tailCmd := exec.Command("tail", "-f", logFile)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		}

		// Otherwise just print the file
		f, err := os.Open(logFile)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer f.Close()

		_, err = io.Copy(os.Stdout, f)
		return err
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)
}
