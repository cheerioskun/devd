# devd

[![CI](https://github.com/your/devd/actions/workflows/ci.yml/badge.svg)](https://github.com/your/devd/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

Lightweight, isolated, movable development workspaces powered by microVMs.

```
$ devd run nicolaka/netshoot --name myapp
INFO Creating workspace "myapp" (nicolaka/netshoot, 2 CPUs, 512 MB)
INFO Prepared ext4 template in 9.63s   # first use of this image digest only
INFO Cloned workspace disk in 24ms
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

devd runs each development workspace in its own microVM using [libkrun](https://github.com/containers/libkrun). Each workspace root is one writable ext4 disk, APFS-cloned (or reflinked on Linux) from an immutable OCI template. Your code stays on the host, mounted into the VM via virtio-fs. Each workspace gets its own Linux kernel, persistent disk, full isolation, and near-native performance — without Docker Desktop or a hidden background VM.

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
- Buildah, e2fsprogs, libkrun, and libkrunfw (`install-runtime` in the project shell)
- A reflink-capable filesystem: APFS on macOS, or reflink support on Linux
- Go 1.25+ and a C compiler (to build the three-binary devd bundle)

## Install

**Option 1: Download bundle (Recommended)**
Download the latest three-binary bundle (`devd`, `devd-vm`, and `devd-image-helper`) for your platform from the [Releases page](https://github.com/your/devd/releases). Keep the companions beside `devd` in the same directory.

**Option 2: Build from source**
```bash
git clone https://github.com/your/devd && cd devd
devenv shell
install-runtime   # first time only
build             # bin/devd + bin/devd-vm + bin/devd-image-helper
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

# Branch complete guest state in milliseconds
devd stop myapp
devd run --name experiment --fork myapp
```

`run --fork` clones the stopped source's ext4 disk, including installed packages,
caches, and `/root` state, then boots the child immediately. It gets fresh ports,
machine ID, and SSH host keys.
The host project mount is reused by default; pass `--mount` to select a different
checkout. Host files are never copied by `fork`.

## Commands

```
devd create [image] --name <n>     Create a workspace (stopped)
devd start <n>                     Boot a stopped workspace
devd run [image] --name <n>        Create + start from an OCI image
devd run --name <n> --fork <src>   Clone a stopped workspace and start it

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
| `--fork` | run | Stopped source workspace to clone and boot |
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

Measured on macOS ARM64 with libkrun 1.19.4 and `nicolaka/netshoot` ([experiments 12–14](experiments/exp14-ext4-product-lifecycle.md)):

| Metric | Value | Notes |
|--------|-------|-------|
| Boot (start → SSH ready) | **~0.2–0.45s** | ext4 root to SSH readiness on a warmed system |
| Cold OCI → ext4 template | ~9.1s | One-time per image digest; cached afterward |
| Cached workspace create | **15–24ms** | Clone one ext4 disk file on APFS |
| Stopped workspace fork | **~19ms** | Clone the source workspace disk |
| Switch latency | <200ms | Next connection routes to new workspace |
| Guest loopback | Isolated | Each VM reaches its own server |

## Project Structure

```
cmd/devd/          CLI entrypoint (cobra)
internal/
  cli/             Command implementations
  config/          Paths and defaults (~/.devd/)
  db/              SQLite state layer (pure Go, no CGO)
  storage/         OCI template cache + ext4 clone lifecycle
  vm/              devd-vm process wrapper + guest init
cmd/devd-vm/       separately linked libkrun runtime companion
cmd/devd-image-helper/ static Linux OCI-to-ext4 metadata copier
  ssh/             SSH keypair + ~/.ssh/config management
  proxy/           Port pre-emption and TCP proxy daemon
experiments/       Networking experiments validating the architecture
```

## Roadmap

| Version | Scope | Status |
|---------|-------|--------|
| **v0.1** | Ext4 lifecycle + switch | **Current** — create/start/run (`--fork`), ssh, stop, rm, daemon, switch |
| v0.1.1 | Default image (Alpine + Nix, ~50MB), `devd pull` for pre-caching | Planned |
| v0.1.2 | devcontainer.json subset (postCreateCommand, forwardPorts, dotfiles) | Planned |
| v0.2 | Portable snapshots | export/import ext4 disk + JSON sidecar |
| v0.3 | Remote storage | `devd snapshot --to s3://...` |
| v0.4 | Remote nodes | `devd create --remote <server>`, agent mode |
| v0.5 | Migration | `devd move myapp --to <server>` |
| v0.6+ | Polish | IDE extension, devenv.yaml generation, `brew install devd` |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0
