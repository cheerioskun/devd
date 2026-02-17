package cli

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "devd",
	Short: "microVM-per-workspace development environments",
	Long: `devd creates isolated microVMs for each development workspace.
One VM per workspace. Sub-second boot. Instant switch.

Inspired by Weaveworks Ignite — but for dev environments, not servers.`,
	SilenceUsage: true,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(psCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(switchCmd)
	rootCmd.AddCommand(daemonCmd)
}
