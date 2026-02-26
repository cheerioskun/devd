# Repository Guidelines

devd: microVM-per-workspace dev environments. Go, no CGO. See `README.md` for usage, `SPEC.md` for design, `CONTRIBUTING.md` for setup.

## Orientation

- Entry point: `cmd/devd/main.go` → `internal/cli/root.go`
- One file per cobra command in `internal/cli/` (e.g., `create.go`, `ssh.go`, `daemon.go`)
- `internal/cli/create.go` has `doCreate()` — shared by both `create` and `run` commands
- `internal/db/db.go` — all queries, migrations, schema in one file
- `internal/proxy/proxy.go` — entire proxy daemon in one file
- `internal/config/config.go` — path constants, defaults
- Networking design rationale and TSI internals: `SPEC.md`
- Empirical validation: `experiments/` (shell scripts + markdown, not Go tests)

## Version control: jj (Jujutsu)

This project uses **jj**, not raw git. All VCS operations must use `jj`.

```bash
jj st                    # status (not git status)
jj diff                  # working copy diff
jj log                   # history
jj new                   # create new change on top of current
jj commit -m "message"   # close current change with description
jj describe -m "message" # set description on working change
jj bookmark set <name>   # like git branch (not jj branch)
```

Do **not** use `git commit`, `git add`, `git checkout`, `git reset`, or `git stash`. jj auto-snapshots the working copy — there is no staging area.

## Hard constraints

1. **No CGO.** Ever. Use `modernc.org/sqlite`, shell out to `krunvm`. Do not link C libraries.
2. **No `krun_set_port_map`.** It breaks guest loopback. See `SPEC.md` appendix.
3. **Daemon must pre-empt contested ports before VMs start.** Ordering matters. See `SPEC.md` networking section.
4. **gofmt only.** No other formatters. Pre-commit hooks enforce `gofmt`, `govet`, `golangci-lint`.

## Dev environment: devenv (Nix)

Managed via `devenv.nix`. Enter with `devenv shell` or automatically via direnv.

- **Go** from nixpkgs (binary cached, tracks latest stable 1.25.x) — `go.mod` declares minimum
- **CGO_ENABLED=0** enforced via devenv — no-CGO invariant is automatic
- **golangci-lint** v2 with project config in `.golangci.yml`
- **jj** (Jujutsu) and **openssh** provided by devenv — no manual install needed
- **krunvm** provided by Nix on Linux; on macOS run `install-krunvm` in the devenv shell
- **Git hooks** (pre-commit): `gofmt`, `govet`, `golangci-lint` — code must pass all three
- **`check` script** runs gofmt + vet + lint + test — use before `jj commit` (hooks may not fire reliably via jj)
- **`devenv test`** runs build, vet, lint, and test — treat failures as blocking

| Command | What |
|---------|------|
| `build` | `go build -o bin/devd ./cmd/devd` |
| `test`  | `go test ./...` |
| `lint`  | `golangci-lint run ./...` |
| `check` | gofmt + vet + lint + test (full pre-commit gate) |
| `setup` | `go mod download` |
| `clean` | `rm -rf bin/` |
| `install-krunvm` | Install krunvm (macOS: brew, Fedora: dnf) |

## Code patterns (match these, don't invent new ones)

**Error handling** — wrap with context, no custom error types:
```go
// good
return fmt.Errorf("create VM: %w", err)

// bad — don't introduce custom error types or sentinels
var ErrVMCreate = errors.New("vm create failed")
```

**DB access** — open per command invocation, close immediately:
```go
// good — every command handler does this
database, err := db.Open()
if err != nil { return err }
defer database.Close()

// bad — don't hold connections across commands or pass db through context
```

**Output** — `fmt.Printf("INFO ...")`, `fmt.Printf("WARN ...")`, `log.Printf("PROXY: ...")`. No structured logging library.

**CLI flags** — package-level vars, registered in `init()`. Create-specific flags use `create*` prefix; others use `flag*`.

**Process detachment** — background krunvm processes use `Setpgid: true` and `go cmd.Wait()`. Do not wait synchronously.

**SSH config** — managed block between `# BEGIN devd-managed` / `# END devd-managed` markers in `~/.ssh/config`. Updated on every create/rm.

## State model

- SQLite (`~/.devd/devd.db`) owns workspace metadata, port reservations, active workspace flag
- krunvm owns actual VM state — devd reconciles via PID liveness checks (`vm.IsRunning`)
- Runtime files live under `~/.devd/workspaces/<name>/` (init script, VM log)
- SSH ports start at 2222, relay ports at 9001; both allocated as `MAX(col) + 1`

## Testing

No tests exist yet. When adding them:
- Standard `testing` package, table-driven, files alongside source
- `db` and `ssh/config.go` are the easiest to unit test (no external deps beyond filesystem)
- `vm` operations need `krunvm` installed — guard with `testing.Short()` or build tags
