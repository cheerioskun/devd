package proxy

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/vm"
)

// Proxy owns declared host ports and forwards connections through OpenSSH
// stream-local tunnels to the selected workspace. Owning every declared port
// before a VM starts keeps TSI behavior stable if the port later becomes
// shared by another workspace.
type Proxy struct {
	db *sql.DB

	mu        sync.Mutex
	listeners map[int]net.Listener
	tunnels   map[string]*Tunnel
	stopCh    chan struct{}
	stopOnce  sync.Once
}

// Tunnel is one host Unix socket forwarded to a guest loopback port.
type Tunnel struct {
	key       string
	Workspace string
	Port      int
	Path      string
	Cmd       *exec.Cmd
	Ready     chan struct{}
	Err       error
}

// New creates a proxy using the supplied database for routing decisions.
func New(database *sql.DB) *Proxy {
	return &Proxy{
		db:        database,
		listeners: make(map[int]net.Listener),
		tunnels:   make(map[string]*Tunnel),
		stopCh:    make(chan struct{}),
	}
}

// EnsurePorts synchronously pre-empts the supplied ports. It returns only
// after every port is owned, which lets the caller safely start a VM.
func (p *Proxy) EnsurePorts(ports []int) error {
	var errs []error
	for _, port := range ports {
		if err := p.ensurePort(port); err != nil {
			errs = append(errs, fmt.Errorf("pre-empt :%d: %w", port, err))
		}
	}
	return errors.Join(errs...)
}

// ReconcilePorts makes the proxy own exactly the supplied set of ports.
func (p *Proxy) ReconcilePorts(ports []int) error {
	desired := make(map[int]bool, len(ports))
	for _, port := range ports {
		desired[port] = true
	}

	p.mu.Lock()
	for port, listener := range p.listeners {
		if desired[port] {
			continue
		}
		delete(p.listeners, port)
		_ = listener.Close()
		log.Printf("PROXY: released 0.0.0.0:%d", port)
	}
	for key, tunnel := range p.tunnels {
		if desired[tunnel.Port] {
			continue
		}
		delete(p.tunnels, key)
		if tunnel.Cmd != nil && tunnel.Cmd.Process != nil {
			_ = tunnel.Cmd.Process.Signal(syscall.SIGTERM)
		}
		_ = os.Remove(tunnel.Path)
	}
	p.mu.Unlock()

	return p.EnsurePorts(ports)
}

func (p *Proxy) ensurePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid TCP port")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stopCh:
		return fmt.Errorf("proxy stopped")
	default:
	}
	if _, exists := p.listeners[port]; exists {
		return nil
	}

	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind %s: %w", address, err)
	}
	p.listeners[port] = listener
	log.Printf("PROXY: managing 0.0.0.0:%d", port)
	go p.acceptLoop(listener, port)
	return nil
}

func (p *Proxy) acceptLoop(listener net.Listener, port int) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-p.stopCh:
				return
			default:
				log.Printf("PROXY: accept on :%d: %v", port, err)
				return
			}
		}
		go p.handleConnection(connection, port)
	}
}

func (p *Proxy) handleConnection(client net.Conn, port int) {
	defer client.Close()

	target, err := p.targetForPort(port)
	if err != nil {
		log.Printf("PROXY: route :%d: %v", port, err)
		return
	}

	var upstream net.Conn
	for attempt := 0; attempt < 2; attempt++ {
		tunnel, tunnelErr := p.ensureTunnel(target, port)
		if tunnelErr != nil {
			err = tunnelErr
			break
		}
		upstream, err = net.DialTimeout("unix", tunnel.Path, 5*time.Second)
		if err == nil {
			break
		}
		p.dropTunnel(tunnel)
	}
	if err != nil {
		log.Printf("PROXY: connect :%d to %s: %v", port, target.Name, err)
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go copyConnection(upstream, client, done)
	go copyConnection(client, upstream, done)
	<-done
	<-done
}

func copyConnection(destination, source net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(destination, source)
	if closeWriter, ok := destination.(interface{ CloseWrite() error }); ok {
		_ = closeWriter.CloseWrite()
	}
	done <- struct{}{}
}

func (p *Proxy) targetForPort(port int) (*db.Workspace, error) {
	workspaces, err := db.GetRunningWorkspacesForPort(p.db, port)
	if err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		if workspace.IsActive {
			return workspace, nil
		}
	}
	if len(workspaces) == 1 {
		return workspaces[0], nil
	}
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("no running workspace declares this port")
	}
	return nil, fmt.Errorf("multiple workspaces declare this port but none is active")
}

func (p *Proxy) ensureTunnel(workspace *db.Workspace, port int) (*Tunnel, error) {
	process, err := vm.ReadProcess(filepath.Join(workspace.WorkspaceDir, "process.json"))
	if err != nil {
		return nil, err
	}
	if process == nil || process.PID != workspace.PID {
		return nil, fmt.Errorf("workspace %q has no matching launch receipt", workspace.Name)
	}
	running, err := process.Running()
	if err != nil {
		return nil, fmt.Errorf("inspect workspace %q process: %w", workspace.Name, err)
	}
	if !running {
		return nil, fmt.Errorf("workspace %q process has exited", workspace.Name)
	}
	key := fmt.Sprintf("%s:%d:%s:%d", workspace.Name, process.PID, process.StartTime, port)
	p.mu.Lock()
	select {
	case <-p.stopCh:
		p.mu.Unlock()
		return nil, fmt.Errorf("proxy stopped")
	default:
	}
	if tunnel := p.tunnels[key]; tunnel != nil {
		p.mu.Unlock()
		<-tunnel.Ready
		return tunnel, tunnel.Err
	}

	path, err := tunnelPath(key)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	tunnel := &Tunnel{
		key:       key,
		Workspace: workspace.Name,
		Port:      port,
		Path:      path,
		Ready:     make(chan struct{}),
	}
	p.tunnels[key] = tunnel
	p.mu.Unlock()

	go p.startTunnel(key, tunnel, workspace)
	<-tunnel.Ready
	return tunnel, tunnel.Err
}

func (p *Proxy) startTunnel(key string, tunnel *Tunnel, workspace *db.Workspace) {
	_ = os.Remove(tunnel.Path)
	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		p.finishTunnelStart(key, tunnel, err)
		return
	}

	forward := fmt.Sprintf("%s:127.0.0.1:%d", tunnel.Path, tunnel.Port)
	command := exec.Command("ssh",
		"-N",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-i", keyPath,
		"-p", strconv.Itoa(workspace.SSHPort),
		"-L", forward,
		"root@127.0.0.1",
	)
	var stderr bytes.Buffer
	command.Stdin = nil
	command.Stderr = &stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		p.finishTunnelStart(key, tunnel, fmt.Errorf("start SSH tunnel: %w", err))
		return
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	p.mu.Lock()
	// Reconciliation or Stop may have removed this pending tunnel while the
	// subprocess was launching. Never publish a process after losing ownership.
	if p.tunnels[key] != tunnel {
		p.mu.Unlock()
		_ = command.Process.Kill()
		p.finishTunnelStart(key, tunnel, fmt.Errorf("tunnel start canceled"))
		return
	}
	tunnel.Cmd = command
	p.mu.Unlock()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-wait:
			p.finishTunnelStart(key, tunnel, tunnelExitError("SSH tunnel exited before ready", err, stderr.String()))
			return
		case <-ticker.C:
			if info, err := os.Stat(tunnel.Path); err == nil && info.Mode()&os.ModeSocket != 0 {
				p.finishTunnelStart(key, tunnel, nil)
				log.Printf("PROXY: tunnel UP — %s :%d", workspace.Name, tunnel.Port)
				err := <-wait
				p.removeTunnel(key, tunnel)
				if err != nil {
					log.Printf("PROXY: tunnel DOWN — %s :%d: %v", workspace.Name, tunnel.Port, tunnelExitError("SSH tunnel exited", err, stderr.String()))
				}
				return
			}
		case <-timeout.C:
			_ = command.Process.Signal(syscall.SIGTERM)
			p.finishTunnelStart(key, tunnel, fmt.Errorf("SSH tunnel was not ready after 5s"))
			return
		case <-p.stopCh:
			_ = command.Process.Signal(syscall.SIGTERM)
			p.finishTunnelStart(key, tunnel, fmt.Errorf("proxy stopped"))
			return
		}
	}
}

func tunnelExitError(message string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if err == nil && stderr == "" {
		return fmt.Errorf("%s", message)
	}
	if err == nil {
		return fmt.Errorf("%s: %s", message, stderr)
	}
	if stderr == "" {
		return fmt.Errorf("%s: %w", message, err)
	}
	return fmt.Errorf("%s: %w: %s", message, err, stderr)
}

func (p *Proxy) finishTunnelStart(key string, tunnel *Tunnel, err error) {
	p.mu.Lock()
	if err != nil && p.tunnels[key] == tunnel {
		delete(p.tunnels, key)
	}
	tunnel.Err = err
	close(tunnel.Ready)
	p.mu.Unlock()
	if err != nil {
		_ = os.Remove(tunnel.Path)
	}
}

func (p *Proxy) dropTunnel(tunnel *Tunnel) {
	key := tunnel.key
	p.mu.Lock()
	if p.tunnels[key] == tunnel {
		delete(p.tunnels, key)
	}
	p.mu.Unlock()
	if tunnel.Cmd != nil && tunnel.Cmd.Process != nil {
		_ = tunnel.Cmd.Process.Signal(syscall.SIGTERM)
	}
	_ = os.Remove(tunnel.Path)
}

func (p *Proxy) removeTunnel(key string, tunnel *Tunnel) {
	p.mu.Lock()
	if p.tunnels[key] == tunnel {
		delete(p.tunnels, key)
	}
	p.mu.Unlock()
	_ = os.Remove(tunnel.Path)
}

func tunnelPath(key string) (string, error) {
	devdDir, err := config.DevdDir()
	if err != nil {
		return "", err
	}
	// Unix socket paths are limited to roughly 104 bytes on macOS. DEVD_DIR is
	// often beneath a long per-user TMPDIR in tests, so sockets use a short,
	// user-private /tmp directory and include the state directory in their hash.
	dir := filepath.Join("/tmp", fmt.Sprintf("devd-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create tunnel directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", fmt.Errorf("secure tunnel directory: %w", err)
	}
	digest := sha256.Sum256([]byte(devdDir + "\x00" + key))
	// Each tunnel incarnation owns a distinct path. Delayed cleanup of an old
	// SSH process must never unlink the replacement tunnel's listening socket.
	file, err := os.CreateTemp(dir, fmt.Sprintf("%x-*.sock", digest[:8]))
	if err != nil {
		return "", fmt.Errorf("allocate tunnel socket path: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// Stop releases all managed ports and SSH tunnels.
func (p *Proxy) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })

	p.mu.Lock()
	defer p.mu.Unlock()
	for port, listener := range p.listeners {
		_ = listener.Close()
		delete(p.listeners, port)
	}
	for key, tunnel := range p.tunnels {
		if tunnel.Cmd != nil && tunnel.Cmd.Process != nil {
			_ = tunnel.Cmd.Process.Signal(syscall.SIGTERM)
		}
		_ = os.Remove(tunnel.Path)
		delete(p.tunnels, key)
	}
}
