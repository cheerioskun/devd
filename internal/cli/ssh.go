package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/workspace"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <workspace> [-- command...]",
	Short: "SSH into a running workspace",
	Long: `Open an SSH session to a running workspace.
Optionally pass a command to execute remotely:

  devd ssh myapp -- echo hello`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE:               runSSH,
}

var shellCmd = &cobra.Command{
	Use:                "shell <workspace> [-- command...]",
	Short:              "Open a shell in a running workspace (alias for ssh)",
	Hidden:             true,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE:               runSSH,
}

func runSSH(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Everything after the workspace name is passed as remote command
	var remoteArgs []string
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			remoteArgs = args[i+1:]
			break
		}
		remoteArgs = append(remoteArgs, args[i])
	}

	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	unlock, err := workspace.Lock(name)
	if err != nil {
		return err
	}
	ws, err := loadWorkspace(database, name)
	unlock() // An interactive SSH session must not prevent stop/rm.
	if err != nil {
		return err
	}
	if ws.State != "running" {
		return fmt.Errorf("workspace %q is stopped; run 'devd start %s' first", name, name)
	}

	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		return err
	}

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-i", keyPath,
		"-p", strconv.Itoa(ws.SSHPort),
		"root@127.0.0.1",
	}
	sshArgs = append(sshArgs, remoteArgs...)

	sshExec := exec.Command("ssh", sshArgs...)
	sshExec.Stdin = os.Stdin
	sshExec.Stdout = os.Stdout
	sshExec.Stderr = os.Stderr

	return sshExec.Run()
}
