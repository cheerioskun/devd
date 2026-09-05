# devd

[![CI](https://github.com/cheerioskun/devd/actions/workflows/ci.yml/badge.svg)](https://github.com/cheerioskun/devd/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

Lightweight, isolated, movable development workspaces powered by microVMs.

```
$ devd run nicolaka/netshoot -n myapp
INFO Preparing image "docker.io/nicolaka/netshoot"...
INFO Using cached ext4 template sha256:f460dc...
INFO Cloned workspace disk in 18ms
INFO Starting workspace "myapp"...
INFO Workspace "myapp" ready

     Image:   docker.io/nicolaka/netshoot
     Mount:   .:/workspace
     SSH:     devd ssh myapp  (or: ssh devd-myapp)
     Create:  0.04s
     Boot:    0.31s

$ devd ssh myapp
root@myapp:~#

$ devd stop myapp
$ devd start myapp

$ devd ps
NAME   IMAGE                        STATE    SSH PORT  CPUS  MEMORY  ACTIVE  CREATED
myapp  docker.io/nicolaka/netshoot  running  2222      2     512 MB  *       5m ago
```

## What is devd?

devd runs each development workspace in its own microVM using [libkrun](https://github.com/containers/libkrun). Each workspace root is one writable ext4 disk, APFS-cloned (or reflinked on Linux) from an immutable OCI template. By default, the current directory stays on the host and is mounted at `/workspace` through virtio-fs. Each workspace gets its own Linux kernel, persistent disk, and near-native performance — without Docker Desktop or a hidden background VM.

Guest disks and kernels are separate, but explicitly mounted host projects remain
shared. Networking isolation also depends on declared-port pre-emption; undeclared
ports retain TSI's host-network behavior.

The current runtime requires images to contain OpenSSH server utilities. A
general-purpose default image and image-independent guest agent are planned;
`nicolaka/netshoot` is the presently tested default.

## Why not Docker / Podman / Dev Containers?

On macOS, every container solution works the same way: spin up a Linux VM, then run containers inside it. Docker Desktop uses LinuxKit. Podman uses Fedora CoreOS (via `podman machine`). Your containers run inside that VM — always.

devd skips the intermediary. libkrun talks directly to Apple's Hypervisor.framework. Each workspace *is* a microVM. One layer, not two.

| | Docker Desktop | Podman | devd |
|---|---|---|---|
| macOS architecture | LinuxKit VM → containers | Fedora VM → containers | Direct microVM per workspace |
| VM layers | 2 (host → VM → container) | 2 (host → VM → container) | 1 (host → microVM) |
| Background overhead | ~2-4GB RAM always | ~1-2GB RAM always | 0 VM RAM; tiny proxy only for declared ports |
| Per-workspace isolation | Shared kernel | Shared kernel | Separate kernel per workspace |
| Workspace switching | N/A | N/A | `devd switch` — instant, zero-disruption |
| Boot to SSH | N/A | ~5-30s (machine start) | **~1s** (measured) |

On Linux, devd uses the same libkrun microVMs (via KVM). Same CLI, same behavior.

## Prerequisites

- macOS on Apple Silicon (binary installer) or Linux (source build currently)
- A reflink-capable filesystem: APFS on macOS, or reflink support on Linux
- Homebrew on macOS; the installer uses it for libkrun, firmware, Buildah, and e2fsprogs

## Install

**Apple Silicon macOS:**

```bash
curl -fsSL https://raw.githubusercontent.com/cheerioskun/devd/main/install.sh | bash
```

The installer downloads and verifies the released three-binary bundle, installs
its Homebrew runtime dependencies, and places `devd`, `devd-vm`, and
`devd-image-helper` together in Homebrew's `bin` directory. Set `DEVD_VERSION`
to install a specific release or `DEVD_INSTALL_DIR` to choose another location.

To inspect before running:

```bash
curl -fsSLO https://raw.githubusercontent.com/cheerioskun/devd/main/install.sh
less install.sh
bash install.sh
```

**Build from source (macOS or Linux):**

```bash
git clone https://github.com/cheerioskun/devd && cd devd
devenv shell
install-runtime   # first time only
build             # bin/devd + bin/devd-vm + bin/devd-image-helper
```

## Quick Start

```bash
# IMAGE is positional; the name is optional.
devd run nicolaka/netshoot -n myapp
devd ssh myapp

# Without -n, devd generates an image-based name such as netshoot-a1b2c3.
devd run nicolaka/netshoot

# Stop and restart the same persistent workspace.
devd stop myapp
devd start myapp

# Branch complete guest state in milliseconds.
devd stop myapp
devd fork myapp -n experiment
```

`fork` clones the stopped source's ext4 disk, including installed packages,
caches, and `/root` state, then boots the child immediately. The child receives
a fresh machine ID and SSH host keys. Its host project mount is reused by
default; pass `--mount` for another checkout or `--no-mount` to omit it. Host
files are never copied.

## Commands

```
devd run [image] [-n name]          Create and start a new workspace
devd start <name>                   Start an existing stopped workspace
devd stop <name>                    Stop a running workspace
devd fork <source> [-n name]        Clone and start a stopped workspace

devd ssh <name> [-- command...]     Open a shell or execute a command
devd ps [-a]                        List running workspaces (or all)
devd logs [-f] <name>               Read VM logs
devd switch <name>                  Route shared declared ports to a workspace
devd rm [-f] <name>                 Remove a workspace and its disk
```

The positional argument to `run` is always an OCI image. `run` always creates a
new workspace; `start` is the explicit counterpart to `stop`.

A workspace can persistently use an external kernel instead of libkrunfw's
embedded default:

```bash
devd run nicolaka/netshoot -n kernel-test --kernel ./arch/arm64/boot/Image
devd stop kernel-test
devd start kernel-test                 # reuses the custom kernel
devd fork kernel-test -n kernel-next   # inherits it
devd fork kernel-test -n stock --kernel=  # returns the child to the embedded kernel
```

The path is resolved to an absolute host path and stored in the workspace spec.
On arm64 it must be a raw Linux `Image`; on x86_64 it must be an ELF kernel.
devd deliberately does not inspect kernel configuration or compatibility. A
custom kernel that cannot provide devd's guest drivers or TSI-backed SSH may
fail to become ready; if its VM remains alive, inspect `devd logs <name>` and
stop it with `devd stop <name>`.

### Lifecycle safety and upgrades

Concurrent operations on the same workspace fail with a busy error; unrelated
workspaces can boot concurrently. Fork locks its stopped source while cloning.
A failed stop blocks forced removal, and a failed boot does not take over active
shared-port routing. Guest management files live in a narrowly scoped control
export—never the global devd directory—and SSH accepts keys, not passwords.

**Stop running workspaces with your previous devd binary before upgrading.**
Stopped version-1 workspace specs upgrade to version 2 on their next boot, using
the current host-supplied init rather than an old script inside the disk. Mixing
binary versions or downgrading upgraded workspaces is unsupported. If a crash
leaves a workspace directory without a database record, devd preserves it and
refuses to reuse that name until you explicitly recover or remove the directory.

### Flags

| Flag | Commands | Description |
|------|----------|-------------|
| `-n, --name` | run, fork | Workspace name; generated when omitted |
| `--cpus` | run, fork | vCPU count (default: 2) |
| `--memory` | run, fork | Memory in MB (default: 512) |
| `-p, --ports` | run, fork | Host ports managed and routed by devd |
| `--mount` | run, fork | Host:guest volume (e.g. `.:/workspace`) |
| `--no-mount` | run, fork | Disable the default/inherited host mount |
| `--cmd` | run, fork | Startup command run after each boot |
| `--kernel` | run, fork | Host path to a custom kernel; inherited by forks |
| `-f` | rm | Stop a running workspace before removal |
| `-a` | ps | Include stopped workspaces |

## Port Routing

Declare host-facing ports when creating a workspace. devd starts its lightweight
proxy automatically and binds each declared port before the VM boots:

```bash
devd run nicolaka/netshoot -n backend -p 8080
devd run nicolaka/netshoot -n frontend -p 8080

devd switch frontend    # host:8080 → frontend
devd switch backend     # host:8080 → backend
```

There is no daemon command or startup ordering to manage. Pre-emption forces TSI
to use real guest sockets for declared ports, so both VMs retain isolated
`localhost:8080`. The host proxy reaches the selected guest through an SSH
stream-local tunnel. Switching affects the next connection and does not restart
VMs or guest processes. Undeclared ports retain libkrun's automatic TSI
exposure.

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

Measured on macOS ARM64 with libkrun 1.19.4 and `nicolaka/netshoot` ([product benchmark](BENCHMARK.md), [experiments 12–14](experiments/exp14-ext4-product-lifecycle.md)):

| Metric | Value | Notes |
|--------|-------|-------|
| Cached `run` → authenticated SSH p95 | **423ms** | complete user-visible operation |
| Stopped `start` → authenticated SSH p95 | **278ms** | complete user-visible operation |
| `fork` → authenticated SSH p95 | **510ms** | clone, fresh identity, and boot |
| Cold OCI → ext4 template | ~9.1s | one-time per image digest; cached afterward |
| APFS disk clone | **15–24ms** | one copy-on-write ext4 file clone |
| Warm switch → correct response p95 | **40ms** | next connection, both tunnels warm |
| Idle RSS | **~207 MiB/VM** | separate kernel, 512 MiB guest allocation |
| Guest loopback | Isolated | each VM reaches its own server |

Run `bash benchmark.sh` for the acceptance benchmark and
`bash test-functional.sh` for the hardware-backed correctness suite.

## Project Structure

```
cmd/devd/          CLI entrypoint (cobra)
internal/
  cli/             Commands, option resolution, shared lifecycle + publication
  config/          Paths and defaults (~/.devd/)
  db/              SQLite state layer (pure Go, no CGO)
  storage/         OCI template cache + ext4 clone lifecycle
  workspace/       Operation locks, authoritative specs, rendered guest inputs
  vm/              Companion adapter, process identities, current guest init
  ssh/             SSH keypair + ~/.ssh/config management
  proxy/           Automatic port pre-emption and routing
cmd/devd-vm/       separately linked libkrun runtime companion
cmd/devd-image-helper/ static Linux OCI-to-ext4 metadata copier
experiments/       Empirical architecture and performance validation
```

## Roadmap

| Version | Scope | Status |
|---------|-------|--------|
| **v0.1** | Ext4 lifecycle + switch | **Current** — run/start/stop/fork, SSH, automatic port routing |
| v0.1.1 | General-purpose default image, `devd pull` for pre-caching | Planned |
| v0.1.2 | devcontainer.json lifecycle hooks and dotfiles | Planned |
| v0.2 | Portable snapshots | export/import ext4 disk + JSON sidecar |
| v0.3 | Remote storage | `devd snapshot --to s3://...` |
| v0.4 | Remote nodes | `devd run --remote <server>`, agent mode |
| v0.5 | Migration | `devd move myapp --to <server>` |
| v0.6+ | Polish | IDE extension, devenv.yaml generation, `brew install devd` |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0
