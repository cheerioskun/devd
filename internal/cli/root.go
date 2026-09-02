package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "devd",
	Short: "microVM-per-workspace development environments",
	Long: `devd runs isolated Linux development workspaces in lightweight microVMs.
Each workspace has a persistent ext4 disk and boots directly on the host hypervisor.`,
	SilenceUsage: true,
	Version:      version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the devd version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("devd %s\n", version)
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.SetVersionTemplate("devd {{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(forkCmd)
	rootCmd.AddCommand(psCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(switchCmd)
	rootCmd.AddCommand(daemonCmd)
}
