package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"devd/internal/config"
)

// Workspace represents a row in the workspaces table.
type Workspace struct {
	Name      string
	Image     string
	RootfsDir string
	SSHPort   int
	RelayPort int
	CPUs      int
	Memory    int
	State     string // stopped | running
	IsActive  bool
	PID       int
	CreatedAt time.Time
}

// Open opens (or creates) the devd SQLite database.
func Open() (*sql.DB, error) {
	path, err := config.DBPath()
	if err != nil {
		return nil, fmt.Errorf("db path: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			name        TEXT PRIMARY KEY,
			image       TEXT NOT NULL,
			rootfs_dir  TEXT NOT NULL DEFAULT '',
			ssh_port    INTEGER NOT NULL UNIQUE,
			relay_port  INTEGER NOT NULL UNIQUE,
			cpus        INTEGER NOT NULL DEFAULT 2,
			memory      INTEGER NOT NULL DEFAULT 512,
			state       TEXT NOT NULL DEFAULT 'stopped',
			is_active   BOOLEAN NOT NULL DEFAULT FALSE,
			pid         INTEGER NOT NULL DEFAULT 0,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS reserved_ports (
			workspace TEXT REFERENCES workspaces(name) ON DELETE CASCADE,
			port      INTEGER NOT NULL,
			PRIMARY KEY (workspace, port)
		);
	`)
	return err
}

// NextSSHPort returns the next available SSH port.
func NextSSHPort(db *sql.DB) (int, error) {
	var maxPort sql.NullInt64
	err := db.QueryRow("SELECT MAX(ssh_port) FROM workspaces").Scan(&maxPort)
	if err != nil {
		return 0, err
	}
	if !maxPort.Valid {
		return config.SSHPortBase, nil
	}
	return int(maxPort.Int64) + 1, nil
}

// NextRelayPort returns the next available relay port.
func NextRelayPort(db *sql.DB) (int, error) {
	var maxPort sql.NullInt64
	err := db.QueryRow("SELECT MAX(relay_port) FROM workspaces").Scan(&maxPort)
	if err != nil {
		return 0, err
	}
	if !maxPort.Valid {
		return config.RelayPortBase, nil
	}
	return int(maxPort.Int64) + 1, nil
}

// CreateWorkspace inserts a new workspace record.
func CreateWorkspace(db *sql.DB, ws *Workspace) error {
	_, err := db.Exec(`
		INSERT INTO workspaces (name, image, rootfs_dir, ssh_port, relay_port, cpus, memory, state, is_active, pid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.Name, ws.Image, ws.RootfsDir, ws.SSHPort, ws.RelayPort,
		ws.CPUs, ws.Memory, ws.State, ws.IsActive, ws.PID,
	)
	return err
}

// SetWorkspaceState updates the state and PID of a workspace.
func SetWorkspaceState(db *sql.DB, name, state string, pid int) error {
	_, err := db.Exec(
		"UPDATE workspaces SET state = ?, pid = ? WHERE name = ?",
		state, pid, name,
	)
	return err
}

// SetActiveWorkspace marks one workspace as active, all others inactive.
func SetActiveWorkspace(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE workspaces SET is_active = FALSE"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE workspaces SET is_active = TRUE WHERE name = ?", name); err != nil {
		return err
	}
	return tx.Commit()
}

// GetWorkspace returns a single workspace by name.
func GetWorkspace(db *sql.DB, name string) (*Workspace, error) {
	ws := &Workspace{}
	err := db.QueryRow(`
		SELECT name, image, rootfs_dir, ssh_port, relay_port, cpus, memory,
		       state, is_active, pid, created_at
		FROM workspaces WHERE name = ?`, name,
	).Scan(
		&ws.Name, &ws.Image, &ws.RootfsDir, &ws.SSHPort, &ws.RelayPort,
		&ws.CPUs, &ws.Memory, &ws.State, &ws.IsActive, &ws.PID, &ws.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("workspace %q not found", name)
	}
	return ws, err
}

// GetActiveWorkspace returns the currently active workspace, if any.
func GetActiveWorkspace(db *sql.DB) (*Workspace, error) {
	ws := &Workspace{}
	err := db.QueryRow(`
		SELECT name, image, rootfs_dir, ssh_port, relay_port, cpus, memory,
		       state, is_active, pid, created_at
		FROM workspaces WHERE is_active = TRUE LIMIT 1`,
	).Scan(
		&ws.Name, &ws.Image, &ws.RootfsDir, &ws.SSHPort, &ws.RelayPort,
		&ws.CPUs, &ws.Memory, &ws.State, &ws.IsActive, &ws.PID, &ws.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ws, err
}

// ListWorkspaces returns all workspaces.
func ListWorkspaces(db *sql.DB) ([]*Workspace, error) {
	rows, err := db.Query(`
		SELECT name, image, rootfs_dir, ssh_port, relay_port, cpus, memory,
		       state, is_active, pid, created_at
		FROM workspaces ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []*Workspace
	for rows.Next() {
		ws := &Workspace{}
		if err := rows.Scan(
			&ws.Name, &ws.Image, &ws.RootfsDir, &ws.SSHPort, &ws.RelayPort,
			&ws.CPUs, &ws.Memory, &ws.State, &ws.IsActive, &ws.PID, &ws.CreatedAt,
		); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, rows.Err()
}

// DeleteWorkspace removes a workspace record.
func DeleteWorkspace(db *sql.DB, name string) error {
	_, err := db.Exec("DELETE FROM workspaces WHERE name = ?", name)
	return err
}

// AddReservedPort adds a reserved port for a workspace.
func AddReservedPort(db *sql.DB, workspace string, port int) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO reserved_ports (workspace, port) VALUES (?, ?)",
		workspace, port,
	)
	return err
}

// GetReservedPorts returns the reserved ports for a workspace.
func GetReservedPorts(db *sql.DB, workspace string) ([]int, error) {
	rows, err := db.Query(
		"SELECT port FROM reserved_ports WHERE workspace = ? ORDER BY port",
		workspace,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, rows.Err()
}

// GetAllContestedPorts returns ports reserved by more than one workspace (any state).
// Used by the daemon to pre-empt ports before VMs start.
func GetAllContestedPorts(db *sql.DB) ([]int, error) {
	rows, err := db.Query(`
		SELECT port
		FROM reserved_ports
		GROUP BY port
		HAVING COUNT(*) > 1
		ORDER BY port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, rows.Err()
}

// GetContestedPorts returns ports reserved by more than one running workspace.
func GetContestedPorts(db *sql.DB) ([]int, error) {
	rows, err := db.Query(`
		SELECT rp.port
		FROM reserved_ports rp
		JOIN workspaces w ON rp.workspace = w.name
		WHERE w.state = 'running'
		GROUP BY rp.port
		HAVING COUNT(*) > 1
		ORDER BY rp.port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ports []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, rows.Err()
}

// GetRunningWorkspacesForPort returns running workspaces that reserve a given port.
func GetRunningWorkspacesForPort(db *sql.DB, port int) ([]*Workspace, error) {
	rows, err := db.Query(`
		SELECT w.name, w.image, w.rootfs_dir, w.ssh_port, w.relay_port,
		       w.cpus, w.memory, w.state, w.is_active, w.pid, w.created_at
		FROM workspaces w
		JOIN reserved_ports rp ON rp.workspace = w.name
		WHERE rp.port = ? AND w.state = 'running'
		ORDER BY w.name`, port,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []*Workspace
	for rows.Next() {
		ws := &Workspace{}
		if err := rows.Scan(
			&ws.Name, &ws.Image, &ws.RootfsDir, &ws.SSHPort, &ws.RelayPort,
			&ws.CPUs, &ws.Memory, &ws.State, &ws.IsActive, &ws.PID, &ws.CreatedAt,
		); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, rows.Err()
}
