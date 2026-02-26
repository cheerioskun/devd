# devd

Lightweight, isolated, movable development workspaces powered by microVMs.

```
$ devd run nicolaka/netshoot --name myapp
INFO Creating workspace "myapp" (nicolaka/netshoot, 2 CPUs, 512 MB)
INFO Created in 13.31s
INFO Starting VM...
INFO Waiting for SSH on port 2222...
INFO SSH ready

     Name:    myapp
     Image:   nicolaka/netshoot
     SSH:     ssh devd-myapp  (or: devd ssh myapp)
     Port:    2222
     Boot:    1.01s

$ devd ssh myapp
root@myapp:~#

$ devd create nicolaka/netshoot --name frontend --ports 8080
$ devd daemon &                    # pre-empts :8080 on host
$ devd start frontend              # TSI falls back — loopback isolated

$ devd switch frontend
INFO Switched active workspace: myapp → frontend
INFO Contested ports [8080] now route to "frontend"

$ curl localhost:8080              # → hits frontend's server

$ devd ps
NAME       IMAGE              STATE    SSH PORT  CPUS  MEMORY  ACTIVE  CREATED
myapp      nicolaka/netshoot  running  2222      2     512 MB          5m ago
frontend   nicolaka/netshoot  running  2223      2     512 MB  *       2m ago
```

## What is devd?

devd runs each development workspace in its own microVM using [libkrun](https://github.com/containers/libkrun). Your code stays on the host, mounted into the VM via virtio-fs. Each workspace gets its own Linux kernel, full isolation, and near-native performance — without Docker Desktop, without a hidden background VM, without 4GB of RAM overhead.

## Why not Docker / Podman / Dev Containers?

On macOS, every container solution works the same way: spin up a Linux VM, then run containers inside it. Docker Desktop uses LinuxKit. Podman uses Fedora CoreOS (via `podman machine`). Your containers run inside that VM — always.

devd skips the intermediary. libkrun talks directly to Apple's Hypervisor.framework. Each workspace *is* a microVM. One layer, not two.

| | Docker Desktop | Podman | devd |
|---|---|---|---|
| macOS architecture | LinuxKit VM → containers | Fedora VM → containers | Direct microVM per workspace |
| VM layers | 2 (host → VM → container) | 2 (host → VM → container) | 1 (host → microVM) |
| Background overhead | ~2-4GB RAM always | ~1-2GB RAM always | 0 when no workspaces running |
| Per-workspace isolation | Shared kernel | Shared kernel | Separate kernel per workspace |
| Workspace switching | N/A | N/A | `devd switch` — instant, zero-disruption |
| Boot to SSH | N/A | ~5-30s (machine start) | **~1s** (measured) |

On Linux, devd uses the same libkrun microVMs (via KVM). Same CLI, same behavior.

## Prerequisites

- macOS (Apple Silicon) or Linux
- [krunvm](https://github.com/containers/krunvm) (`brew install krunvm` on macOS)
- Go 1.22+ (to build from source)

## Install

```bash
git clone https://github.com/your/devd && cd devd
go build -o bin/devd ./cmd/devd
```

## Quick Start

```bash
# Create and start a workspace in one command
devd run nicolaka/netshoot --name myapp
devd ssh myapp

# Step-by-step (use this when you need multi-workspace port routing)
devd create nicolaka/netshoot --name myapp --ports 8080
devd daemon &        # only needed when multiple workspaces share a port
devd start myapp
```

## Commands

```
devd create [image] --name <n>     Create a workspace (stopped)
devd start <n>                     Boot a stopped workspace
devd run [image] --name <n>        Create + start in one step

devd ps [-a]                       List workspaces (running, or all)
devd ssh <n>                       SSH into a running workspace
devd shell <n>                     Alias for ssh
devd stop <n>                      Stop a running workspace
devd rm [-f] <n>                   Remove a workspace

devd daemon [--ports 8080,3000]    Run the proxy daemon (pre-empts contested ports)
devd switch <n>                    Route contested ports to this workspace
```

### Flags

| Flag | Commands | Description |
|------|----------|-------------|
| `--name` | create, run | Workspace name (required) |
| `--cpus` | create, run | vCPU count (default: 2) |
| `--memory` | create, run | Memory in MB (default: 512) |
| `--ports` | create, run | Ports to reserve (for proxy routing) |
| `--mount` | create, run | Host:guest volume (e.g. `.:/workspace`) |
| `--cmd` | create, run | Command to run inside VM after boot |
| `-f` | rm | Force remove (stop if running) |
| `-a` | ps | Show all workspaces including stopped |

## Multi-Workspace Port Routing

When multiple workspaces claim the same port, devd handles it with a proxy-based architecture validated in [experiments 4–7](experiments/):

1. **`devd daemon`** binds `0.0.0.0:<contested-port>` on the host before VMs start
2. This **pre-empts TSI** — libkrun's host-side bind fails, so guest kernels fall back to real sockets
3. **Guest loopback is isolated** — `curl localhost:8080` inside each VM reaches that VM's own server
4. **SSH tunnels** relay from host relay ports to each VM's `localhost:<port>`
5. **`devd switch`** changes which tunnel the proxy routes to — instant, no processes killed

```
                    ┌─────────────┐
  browser/curl ───► │ devd daemon │
                    │ 0.0.0.0:8080│
                    └──────┬──────┘
                           │ routes to active workspace
                    ┌──────┴──────┐
                    ▼             ▼
           SSH tunnel:9001  SSH tunnel:9002
           (host-side)      (host-side)
                │                │
                ▼                ▼
          VM-A :2222       VM-B :2223
          (sshd via TSI)   (sshd via TSI)
                │                │
                ▼                ▼
          VM-A localhost   VM-B localhost
          :8080 (local)    :8080 (local)
          loopback ✓       loopback ✓
```

The correct workflow for contested ports:

```bash
devd create nicolaka/netshoot --name backend --ports 8080
devd create nicolaka/netshoot --name frontend --ports 8080
devd daemon &           # pre-empts :8080 BEFORE VMs start
devd start backend
devd start frontend
devd switch frontend    # host:8080 → frontend
devd switch backend     # host:8080 → backend
```

Ports that only one workspace uses are auto-exposed by TSI — no daemon needed.

## IDE Integration

devd exposes an SSH server in each workspace and manages `~/.ssh/config` automatically:

```bash
# VS Code
code --remote ssh-remote+devd-myapp /workspace

# Cursor
cursor --remote ssh-remote+devd-myapp /workspace

# Any SSH-capable editor
ssh devd-myapp
```

When you create a workspace, `Host devd-<name>` appears in your SSH config. When you remove it, the entry is cleaned up.

## Performance

Measured on macOS ARM64, krunvm 0.2.6, nicolaka/netshoot image ([experiment 8](experiments/exp8-devd-boot-and-switch.md)):

| Metric | Value | Notes |
|--------|-------|-------|
| Boot (start → SSH ready) | **~1.0s** | Measured across multiple runs; exp8 median 0.61s on a warmed system |
| Create (OCI extraction) | ~10–16s | One-time cost per workspace; varies with image size and host I/O |
| Switch latency | <200ms | Next connection routes to new workspace |
| Guest loopback | Isolated | Each VM reaches its own server |

## Project Structure

```
cmd/devd/          CLI entrypoint (cobra)
internal/
  cli/             Command implementations
  config/          Paths and defaults (~/.devd/)
  db/              SQLite state layer (pure Go, no CGO)
  vm/              krunvm wrapper (create/start/stop/delete)
  ssh/             SSH keypair + ~/.ssh/config management
  proxy/           Port pre-emption and TCP proxy daemon
experiments/       Networking experiments validating the architecture
```

## Roadmap

| Version | Scope | Status |
|---------|-------|--------|
| **v0.1** | Local lifecycle + switch | **Current** — create/start/run, ssh, stop, rm, daemon, switch |
| v0.1.1 | Default image (Alpine + Nix, ~50MB), `devd pull` for pre-caching | Planned |
| v0.1.2 | devcontainer.json subset (postCreateCommand, forwardPorts, dotfiles) | Planned |
| v0.2 | Snapshots | `devd snapshot` → tarball + JSON sidecar, `devd restore` |
| v0.3 | Remote storage | `devd snapshot --to s3://...` |
| v0.4 | Remote nodes | `devd create --remote <server>`, agent mode |
| v0.5 | Migration | `devd move myapp --to <server>` |
| v0.6+ | Polish | IDE extension, devenv.yaml generation, `brew install devd` |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0
