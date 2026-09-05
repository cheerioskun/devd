# Repository Guidelines

devd: microVM-per-workspace dev environments. Go, no CGO. See `README.md` for usage, `SPEC.md` for design, `CONTRIBUTING.md` for setup.

## Orientation

- Entry point: `cmd/devd/main.go` → `internal/cli/root.go`
- One file per public cobra command in `internal/cli/` (e.g., `run.go`, `ssh.go`, `daemon.go`)
- `internal/cli/options.go` resolves inputs and fork inheritance before side effects
- `internal/cli/provision.go` owns the staged disk/spec/DB publisher shared by `run` and `fork`
- `internal/cli/lifecycle.go` owns reconciliation, start/readiness/activation, and confirmed stop
- `internal/db/db.go` — all queries and schema in one file
- `internal/storage/storage.go` — digest cache, ext4 templates, reflink cloning
- `internal/workspace/spec.go` — authoritative boot specs and disposable guest inputs
- `internal/workspace/lock.go` — fail-fast per-workspace operation locks, acquired before reading state
- `internal/vm/runtime.go` — shells out to the bundled `devd-vm` companion
- `internal/vm/process*.go` — durable launch receipts and OS process birth identities
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

1. **No CGO in Go.** Ever. Use `modernc.org/sqlite`; shell out to the separately linked `devd-vm` companion. Do not link C libraries into the Go binary.
2. **No `krun_set_port_map`.** It breaks guest loopback. See `SPEC.md` appendix.
3. **The automatic proxy must pre-empt every declared port before its VM starts.** This preserves guest loopback and makes later sharing safe. See `SPEC.md` networking section.
4. **gofmt only.** No other formatters. Pre-commit hooks enforce `gofmt`, `govet`, `golangci-lint`.

## Dev environment: devenv (Nix)

Managed via `devenv.nix`. Enter with `devenv shell` or automatically via direnv.

- **Go** from nixpkgs (binary cached, tracks latest stable 1.25.x) — `go.mod` declares minimum
- **CGO_ENABLED=0** enforced via devenv — no-CGO invariant is automatic
- **golangci-lint** v2 with project config in `.golangci.yml`
- **jj** (Jujutsu) and **openssh** provided by devenv — no manual install needed
- **Buildah/libkrun/e2fsprogs** provided by Nix on Linux; on macOS run `install-runtime` in the devenv shell
- **Git hooks** (pre-commit): `gofmt`, `govet`, `golangci-lint` — code must pass all three
- **`check` script** runs gofmt + vet + lint + test — use before `jj commit` (hooks may not fire reliably via jj)
- **`devenv test`** runs build, vet, lint, and test — treat failures as blocking

| Command | What |
|---------|------|
| `build` | Build `bin/devd`, `bin/devd-vm`, and `bin/devd-image-helper` |
| `test`  | `go test ./...` |
| `lint`  | `golangci-lint run ./...` |
| `check` | gofmt + vet + lint + test (full pre-commit gate) |
| `setup` | `go mod download` |
| `clean` | `rm -rf bin/` |
| `install-runtime` | Install Buildah, e2fsprogs, libkrun, and firmware |

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

**CLI flags** — package-level vars, registered in `init()`. Shared run flags use `flag*`; command-specific flags use the command name as their prefix.

**Process detachment** — background `devd-vm` processes use `Setpgid: true` and `go cmd.Wait()`. Do not wait synchronously.

**SSH config** — managed block between `# BEGIN devd-managed` / `# END devd-managed` markers in `~/.ssh/config`. Updated on every create/rm.

## State model

- SQLite (`~/.devd/devd.db`) owns workspace metadata, port reservations, active workspace flag
- One writable `rootfs.ext4` owns each workspace's persistent guest state
- `process.json` records PID + OS birth identity; `loadWorkspace` reconciles it under the workspace lock
- The companion holds an exclusive root-disk inode lock for its entire lifetime; clone/removal also require it
- Only `workspaces/<name>/control/` is exported to that guest; never export the global state directory
- Spec v2 is authoritative; guest inputs and the current host-supplied init are rendered before each boot
- Acquire workspace locks first, then the short DB metadata lock for publication/allocation/SSH config; never hold the metadata lock while waiting for a VM
- Stop failure blocks deletion. Readiness, process liveness, and active routing are distinct
- Runtime files live under `~/.devd/workspaces/<name>/` (ext4 disk, config, VM log)
- Immutable digest-addressed templates live under `~/.devd/images/`
- SSH ports start at 2222, advance from `MAX(ssh_port) + 1`, and skip ports already bound on the host
- `relay_port` remains in schema v2 for compatibility but routing now uses short OpenSSH stream-local sockets

## Testing

- Unit tests use the standard `testing` package and live beside source; run `test` or `go test ./...`
- `check` is the required formatting, vet, lint, and unit-test gate
- `bash test-functional.sh` is the isolated hardware-backed product regression suite
- `bash benchmark.sh` enforces the user-visible latency and idle-RSS envelope documented in `BENCHMARK.md`
- Full VM/storage tests require the runtime companions, libkrun, Buildah, and e2fsprogs; keep them out of `go test ./...`

## Experiments

The `experiments/` directory contains empirical validation of design decisions — shell scripts, markdown writeups, and sometimes prototype code. **Before making assumptions about performance, networking, or VM behavior, check here first.**

### When to consult experiments/

- You are unsure why a technical constraint exists (e.g., "why not use overlayfs?", "why avoid krun_set_port_map?")
- You are debugging unexpected behavior in boot, networking, SSH, or port routing
- You want to understand the measured performance baseline before optimizing
- You are considering a new architecture (check if it was already tried and abandoned)

### What exists

| Experiment | What it validates |
|------------|-------------------|
| exp4–exp7  | Proxy/TSI architecture and port-routing correctness |
| exp8       | Boot time benchmark (median 0.61s) + switch validation (14/14 PASS). Also reveals SSH key auth fix: `chmod 755 /root` needed for netshoot image |
| exp9       | VM density under memory pressure |
| exp10      | `krunvm create` bottleneck = `buildah VFS` full copy. Optimization tiers documented (small image → APFS template clone → erofs direct) |
| exp11      | `devd-vm` C prototype and directory-clone baseline |
| exp12      | APFS-cloned ext4 root disks: metadata, identity, isolation, persistence, recovery |
| exp13      | ext4 guest performance versus directory-root virtio-fs |
| exp14      | Product ext4 run/start and `fork` lifecycle |
| exp15      | Explicit per-VM kernels with ext4 root, initramfs, and custom cmdline |
| exp16      | Workspace/disk ownership, scoped guest control, current host bootstrap, and lifecycle failure paths |

### When to add new experiments

If a challenge is non-trivial and you cannot resolve it by reading the code — **add an experiment**:

1. Create `experiments/expN-short-description.sh` (or `.md`) where N is the next number
2. Add a corresponding `experiments/expN-short-description.md` writeup with: hypothesis, method, results, conclusion
3. Reference the experiment in any code comment or commit message where the decision is made

This keeps institutional knowledge in the repo, not just in conversation history.
