package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitializeSchemaResetsPreExt4Database(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.Exec(`
		CREATE TABLE workspaces (
			name TEXT PRIMARY KEY,
			image TEXT NOT NULL,
			rootfs_dir TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO workspaces (name, image, rootfs_dir) VALUES ('legacy', 'alpine', '/tmp/root');
	`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(database); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM workspaces").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy rows remain after clean-slate migration: %d", count)
	}
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestWorkspaceRoundTrip(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := initializeSchema(database); err != nil {
		t.Fatal(err)
	}

	input := &Workspace{
		Name:         "child",
		Image:        "docker.io/library/alpine",
		ImageDigest:  "sha256:abc",
		WorkspaceDir: "/tmp/child",
		DiskPath:     "/tmp/child/rootfs.ext4",
		ParentName:   "parent",
		SSHPort:      2222,
		RelayPort:    9001,
		CPUs:         2,
		Memory:       512,
		State:        "stopped",
	}
	if err := CreateWorkspace(database, input); err != nil {
		t.Fatal(err)
	}
	output, err := GetWorkspace(database, input.Name)
	if err != nil {
		t.Fatal(err)
	}
	if output.DiskPath != input.DiskPath || output.ImageDigest != input.ImageDigest || output.ParentName != input.ParentName {
		t.Fatalf("workspace round trip = %#v", output)
	}
}
