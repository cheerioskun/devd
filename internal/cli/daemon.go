package cli

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"devd/internal/db"
	"devd/internal/proxy"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the proxy daemon for contested port routing",
	Long: `Start the devd proxy daemon. This MUST be started BEFORE VMs that share
ports, so it can pre-empt those ports before TSI grabs them.

The daemon:
  1. Reads reserved ports from the database to find contested ports
  2. Binds 0.0.0.0:<port> for each contested port (pre-empts TSI)
  3. Polls for running VMs and sets up SSH tunnels as they come online
  4. Proxies incoming connections to the active workspace's tunnel

Correct workflow:
  devd create --name backend --ports 8080 ...
  devd create --name frontend --ports 8080 ...
  devd daemon &                              # pre-empts :8080
  devd start backend                         # TSI falls back on :8080
  devd start frontend                        # TSI falls back on :8080
  devd switch frontend                       # proxy routes to frontend`,
	RunE: runDaemon,
}

var daemonPorts []int

func init() {
	daemonCmd.Flags().IntSliceVar(&daemonPorts, "ports", nil,
		"explicit ports to pre-empt (overrides auto-detection from DB)")
}

func runDaemon(cmd *cobra.Command, args []string) error {
	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	// Determine which ports to pre-empt
	var contestedPorts []int
	if len(daemonPorts) > 0 {
		contestedPorts = daemonPorts
	} else {
		// Auto-detect: ports reserved by 2+ workspaces (any state)
		contestedPorts, err = db.GetAllContestedPorts(database)
		if err != nil {
			return fmt.Errorf("detect contested ports: %w", err)
		}
	}

	if len(contestedPorts) == 0 {
		fmt.Println("INFO No contested ports found. Nothing to proxy.")
		fmt.Println("INFO Create workspaces with --ports first, then re-run daemon.")
		return nil
	}

	p := proxy.New(database)

	// Phase 1: Pre-empt ports BEFORE any VMs start
	fmt.Printf("INFO Pre-empting ports: %v\n", contestedPorts)
	if err := p.PreemptPorts(contestedPorts); err != nil {
		return fmt.Errorf("pre-empt ports: %w", err)
	}
	fmt.Println("INFO Ports pre-empted. Safe to start VMs now.")

	// Phase 2: Poll for running VMs and set up tunnels
	go p.PollTunnels(contestedPorts)

	fmt.Println("INFO Proxy daemon running. Press Ctrl-C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig

	log.Printf("PROXY: received %v, shutting down...", s)
	p.Stop()
	fmt.Println("INFO Proxy daemon stopped")
	return nil
}
