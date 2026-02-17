package proxy

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"devd/internal/config"
	"devd/internal/db"
)

// Proxy manages TCP proxying for contested ports and SSH tunnel lifecycle.
type Proxy struct {
	db        *sql.DB
	listeners map[int]net.Listener // contested port → listener
	tunnels   map[string]*Tunnel   // "workspace:port" → SSH tunnel
	mu        sync.Mutex
	stopCh    chan struct{}
}

// Tunnel represents an SSH port-forwarding tunnel.
type Tunnel struct {
	LocalPort  int // relay port on host
	RemotePort int // contested port inside guest
	SSHPort    int // guest sshd port (TSI-exposed)
	Cmd        *exec.Cmd
}

// New creates a new Proxy.
func New(database *sql.DB) *Proxy {
	return &Proxy{
		db:        database,
		listeners: make(map[int]net.Listener),
		tunnels:   make(map[string]*Tunnel),
		stopCh:    make(chan struct{}),
	}
}

// PreemptPorts binds the given ports on 0.0.0.0 BEFORE any VMs start.
// This blocks TSI from grabbing them, forcing guests to fall back to
// real kernel sockets (see experiments 4-6).
func (p *Proxy) PreemptPorts(ports []int) error {
	for _, port := range ports {
		if err := p.listen(port); err != nil {
			return fmt.Errorf("pre-empt :%d: %w", port, err)
		}
	}
	return nil
}

// PollTunnels periodically scans the DB for running workspaces and sets up
// SSH tunnels to any that are new. Call this in a goroutine after pre-emption.
func (p *Proxy) PollTunnels(contestedPorts []int) {
	for {
		select {
		case <-p.stopCh:
			return
		case <-time.After(2 * time.Second):
		}

		for _, port := range contestedPorts {
			workspaces, err := db.GetRunningWorkspacesForPort(p.db, port)
			if err != nil {
				continue
			}
			for _, ws := range workspaces {
				p.ensureTunnel(ws, port)
			}
		}
	}
}

func (p *Proxy) ensureTunnel(ws *db.Workspace, contestedPort int) {
	key := fmt.Sprintf("%s:%d", ws.Name, contestedPort)
	p.mu.Lock()
	if _, exists := p.tunnels[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	// Verify SSH is reachable before setting up tunnel
	addr := fmt.Sprintf("127.0.0.1:%d", ws.SSHPort)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return // VM not ready yet, will retry next poll
	}
	conn.Close()

	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		log.Printf("PROXY: ssh key path: %v", err)
		return
	}

	cmd := exec.Command("ssh",
		"-N",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ExitOnForwardFailure=yes",
		"-i", keyPath,
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", ws.RelayPort, contestedPort),
		"root@127.0.0.1",
		"-p", strconv.Itoa(ws.SSHPort),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		log.Printf("PROXY: tunnel %s failed to start: %v", key, err)
		return
	}
	go cmd.Wait()

	tunnel := &Tunnel{
		LocalPort:  ws.RelayPort,
		RemotePort: contestedPort,
		SSHPort:    ws.SSHPort,
		Cmd:        cmd,
	}

	p.mu.Lock()
	p.tunnels[key] = tunnel
	p.mu.Unlock()

	log.Printf("PROXY: tunnel UP — %s — localhost:%d → ssh:%d → VM localhost:%d",
		ws.Name, ws.RelayPort, ws.SSHPort, contestedPort)
}

func (p *Proxy) listen(port int) error {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	p.listeners[port] = ln
	log.Printf("PROXY: pre-empted 0.0.0.0:%d (TSI will fall back to real sockets)", port)

	go p.acceptLoop(ln, port)
	return nil
}

func (p *Proxy) acceptLoop(ln net.Listener, contestedPort int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
				return
			default:
				log.Printf("PROXY: accept on :%d: %v", contestedPort, err)
				return
			}
		}
		go p.handleConn(conn, contestedPort)
	}
}

func (p *Proxy) handleConn(client net.Conn, contestedPort int) {
	defer client.Close()

	active, err := db.GetActiveWorkspace(p.db)
	if err != nil || active == nil {
		log.Printf("PROXY: no active workspace for :%d", contestedPort)
		return
	}

	target := fmt.Sprintf("127.0.0.1:%d", active.RelayPort)
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		log.Printf("PROXY: connect to %s (%s): %v", target, active.Name, err)
		return
	}
	defer upstream.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
	}()
	wg.Wait()
}

// Stop tears down all tunnels and listeners.
func (p *Proxy) Stop() {
	close(p.stopCh)

	p.mu.Lock()
	defer p.mu.Unlock()

	for key, t := range p.tunnels {
		if t.Cmd.Process != nil {
			t.Cmd.Process.Signal(syscall.SIGTERM)
		}
		delete(p.tunnels, key)
	}

	for port, ln := range p.listeners {
		ln.Close()
		delete(p.listeners, port)
	}
}
