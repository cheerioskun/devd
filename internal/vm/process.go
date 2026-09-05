package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Process identifies a particular process incarnation, not just a reusable PID.
// The receipt is published before Start returns, so a later invocation can
// recover a launch even if the CLI exited before updating SQLite.
type Process struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time"`
}

// Running reports whether this exact process is still alive. Observation errors
// are not interpreted as successful shutdown.
func (p Process) Running() (bool, error) {
	if p.PID <= 0 || p.StartTime == "" {
		return false, fmt.Errorf("incomplete VM process identity")
	}
	identity, err := processIdentity(p.PID)
	return identity != "" && identity == p.StartTime, err
}

// ReadProcess reads the launch receipt; a missing receipt denotes a workspace
// that has never been started by this runtime version.
func ReadProcess(path string) (*Process, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VM process receipt: %w", err)
	}
	var process Process
	if err := json.Unmarshal(data, &process); err != nil {
		return nil, fmt.Errorf("decode VM process receipt: %w", err)
	}
	if process.PID <= 0 || process.StartTime == "" {
		return nil, fmt.Errorf("invalid VM process receipt %s", path)
	}
	return &process, nil
}

func writeProcess(path string, process Process) error {
	data, err := json.Marshal(process)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".process-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
