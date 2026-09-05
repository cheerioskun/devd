package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spf13/cobra"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/proxy"
)

var daemonCmd = &cobra.Command{
	Use:    "daemon",
	Short:  "Run the internal port proxy",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runDaemon,
}

type daemonRequest struct {
	Command string `json:"command"`
	Ports   []int  `json:"ports,omitempty"`
}

type daemonResponse struct {
	Error string `json:"error,omitempty"`
}

func runDaemon(cmd *cobra.Command, args []string) error {
	lockPath, err := config.DaemonLockPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open daemon lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("proxy daemon is already running")
	}

	socketPath, err := config.DaemonSocketPath()
	if err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale daemon socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on daemon socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0600); err != nil {
		return fmt.Errorf("secure daemon socket: %w", err)
	}

	database, err := db.Open()
	if err != nil {
		return err
	}
	defer database.Close()

	portProxy := proxy.New(database)
	defer portProxy.Stop()
	if ports, portsErr := db.GetAllReservedPorts(database); portsErr != nil {
		return fmt.Errorf("read reserved ports: %w", portsErr)
	} else if ensureErr := portProxy.EnsurePorts(ports); ensureErr != nil {
		log.Printf("PROXY: initial port reconciliation: %v", ensureErr)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go serveDaemonControl(listener, portProxy)
	go reconcileDaemonPorts(database, portProxy)
	log.Printf("PROXY: daemon ready (socket %s)", socketPath)

	received := <-signals
	log.Printf("PROXY: received %v, shutting down", received)
	return nil
}

func serveDaemonControl(listener net.Listener, portProxy *proxy.Proxy) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go handleDaemonControl(connection, portProxy)
	}
}

func handleDaemonControl(connection net.Conn, portProxy *proxy.Proxy) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))

	var request daemonRequest
	response := daemonResponse{}
	shutdown := false
	if err := json.NewDecoder(connection).Decode(&request); err != nil {
		response.Error = fmt.Sprintf("decode request: %v", err)
	} else {
		switch request.Command {
		case "ensure":
			if err := portProxy.EnsurePorts(request.Ports); err != nil {
				response.Error = err.Error()
			}
		case "shutdown":
			shutdown = true
		default:
			response.Error = fmt.Sprintf("unknown daemon command %q", request.Command)
		}
	}
	_ = json.NewEncoder(connection).Encode(response)
	if shutdown {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}
}

func reconcileDaemonPorts(database *sql.DB, portProxy *proxy.Proxy) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		// Snapshot and application are one metadata critical section. Otherwise
		// an old snapshot could release a newly pre-empted port just before boot.
		unlock, err := db.LockMetadata()
		if err != nil {
			log.Printf("PROXY: lock port reconciliation: %v", err)
			continue
		}
		ports, err := db.GetAllReservedPorts(database)
		if err == nil {
			err = portProxy.ReconcilePorts(ports)
		}
		unlock()
		if err != nil {
			log.Printf("PROXY: reconcile ports: %v", err)
		}
	}
}

func ensureProxyDaemon(ports []int) error {
	if len(ports) == 0 {
		return nil
	}
	connected, err := requestProxyDaemon(ports)
	if connected {
		return err
	}

	fmt.Printf("INFO Starting port proxy for %v...\n", ports)
	if err := startProxyDaemonProcess(); err != nil {
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		connected, requestErr := requestProxyDaemon(ports)
		if connected {
			return requestErr
		}
		lastErr = requestErr
		time.Sleep(50 * time.Millisecond)
	}
	logPath, _ := config.DaemonLogPath()
	return fmt.Errorf("proxy daemon did not become ready: %v (log: %s)", lastErr, logPath)
}

func requestProxyDaemon(ports []int) (bool, error) {
	return sendProxyDaemonRequest(daemonRequest{Command: "ensure", Ports: ports})
}

// shutdownProxyDaemon is called under the metadata lock after deleting the last
// declaration. Wait for socket removal before allowing a new workspace to be
// published, so it cannot successfully ensure ports on a daemon that is exiting.
func shutdownProxyDaemon() error {
	connected, err := sendProxyDaemonRequest(daemonRequest{Command: "shutdown"})
	if !connected {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := config.DaemonSocketPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("proxy daemon did not remove its control socket after shutdown")
}

func sendProxyDaemonRequest(request daemonRequest) (bool, error) {
	socketPath, err := config.DaemonSocketPath()
	if err != nil {
		return false, err
	}
	connection, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))

	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return true, fmt.Errorf("send proxy daemon request: %w", err)
	}
	var response daemonResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return true, fmt.Errorf("read proxy daemon response: %w", err)
	}
	if response.Error != "" {
		return true, fmt.Errorf("prepare declared ports: %s", response.Error)
	}
	return true, nil
}

func startProxyDaemonProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate devd executable: %w", err)
	}
	logPath, err := config.DaemonLogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open proxy daemon log: %w", err)
	}

	process := exec.Command(executable, "daemon")
	process.Stdin = nil
	process.Stdout = logFile
	process.Stderr = logFile
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := process.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start proxy daemon: %w", err)
	}
	_ = logFile.Close()
	if err := process.Process.Release(); err != nil {
		return fmt.Errorf("detach proxy daemon: %w", err)
	}
	return nil
}
