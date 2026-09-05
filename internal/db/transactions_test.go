package db

import "testing"

func TestWorkspaceAndPortsPublishAtomically(t *testing.T) {
	t.Setenv("DEVD_DIR", t.TempDir())
	database, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ws := &Workspace{Name: "test", SSHPort: 2222, RelayPort: 9001, State: "stopped"}
	// A duplicate declaration fails after insertion of the workspace and first
	// port. The transaction must roll back both, not leave a partial workspace.
	if err := CreateWorkspace(database, ws, 8080, 8080); err == nil {
		t.Fatal("duplicate port did not fail")
	}
	if exists, err := WorkspaceExists(database, ws.Name); err != nil || exists {
		t.Fatalf("failed publication left record: %v, %v", exists, err)
	}
	if ports, err := GetAllReservedPorts(database); err != nil || len(ports) != 0 {
		t.Fatalf("failed publication left ports: %v, %v", ports, err)
	}
	if err := CreateWorkspace(database, ws, 8080, 9090); err != nil {
		t.Fatal(err)
	}
}

func TestActivationPreservesCurrentOnInvalidTarget(t *testing.T) {
	t.Setenv("DEVD_DIR", t.TempDir())
	database, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := CreateWorkspace(database, &Workspace{Name: "healthy", SSHPort: 2222, RelayPort: 9001, State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := CreateWorkspace(database, &Workspace{Name: "stopped", SSHPort: 2223, RelayPort: 9002, State: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if err := SetActiveWorkspace(database, "healthy"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "stopped"} {
		if err := SetActiveWorkspace(database, name); err == nil {
			t.Fatalf("activated %s", name)
		}
		active, err := GetActiveWorkspace(database)
		if err != nil || active == nil || active.Name != "healthy" {
			t.Fatalf("invalid target cleared activation: %#v, %v", active, err)
		}
	}
}
