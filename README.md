# devd

Lightweight, isolated, movable development workspaces powered by microVMs.

```
$ devd new myapp
Creating workspace "myapp"... done (0.8s)

$ devd shell myapp
dev@myapp:/workspace$ devenv up
Starting processes... api on :8080, db on :5432

$ devd new frontend
Creating workspace "frontend"... done (0.6s)

$ devd switch frontend
▶ frontend is active (3ms)

$ curl localhost:8080   # → hits frontend's server now

$ devd list
  NAME       STATUS     PORTS        IMAGE
▶ frontend   running    :3000 :8080  alpine-nix:latest
  myapp      running    :8080 :5432  alpine-nix:latest

$ devd snapshot myapp
Snapshot saved: ~/.devd/snapshots/myapp-20260215.tar.zst (142MB)
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
| Snapshots & migration | ✗ | ✗ | ✓ (v0.2+) |
| VS Code support | Dev Containers plugin | Dev Containers plugin | Remote-SSH |

On Linux, devd uses the same libkrun microVMs (via KVM). Same CLI, same behavior, same workspaces — portable between your Mac and your Linux server.

## Install

```bash
# macOS (Apple Silicon)
brew install devd

# Linux
curl -fsSL https://devd.dev/install.sh | sh
```

## Quick Start

```bash
# Create and enter a workspace
devd new myapp
devd shell myapp

# Inside the workspace, use devenv or whatever you like
cd /workspace
devenv init
devenv up
```

## Commands

```
devd new <n> [--image <image>]     Create and start a workspace
devd shell <n>                     Attach to workspace shell
devd list                          Show all workspaces and active ports
devd stop <n>                      Stop a workspace
devd rm <n>                        Stop and delete a workspace

devd switch <n>                    Route managed ports to this workspace
                                   All VMs keep running. Instant. Zero disruption.

devd snapshot <n> [--to <path>]    Snapshot workspace state             (v0.2)
devd restore <n> --from <path>     Restore from snapshot                (v0.2)
devd move <n> --to <server>        Migrate workspace to remote          (v0.5)
```

## How It Works

Your project directory is mounted into the VM at `/workspace` via virtio-fs. Changes are instant and bidirectional — there is no sync, no copy, no delay.

**Networking** uses libkrun's TSI (Transparent Socket Impersonation). Sockets inside the VM are transparently proxied to the host. Start a server on `:8080` inside the VM — it's reachable from your host at `localhost:8080`. `curl localhost:8080` inside the VM also works (guest loopback). Zero config.

**Multi-workspace port routing** is handled by `devd switch`. When multiple workspaces bind the same port, TSI lets them coexist on the host — the kernel routes to one. `devd switch` tells the non-active workspace's server to pause, and the active workspace takes over the port instantly. All VMs keep running. Coding agents, build processes, background jobs are undisturbed.

Ports that don't conflict across workspaces are auto-exposed by TSI simultaneously — no switching needed.

## IDE Integration

devd exposes an SSH server in each workspace. Connect with any editor that supports Remote-SSH:

```bash
# VS Code
code --remote ssh-remote+devd-myapp /workspace

# Cursor
cursor --remote ssh-remote+devd-myapp /workspace

# Any SSH-capable editor
ssh devd-myapp
```

devd manages `~/.ssh/config` entries automatically. When you `devd new myapp`, an SSH host `devd-myapp` appears. When you `devd rm myapp`, it's cleaned up.

## Configuration

devd reads `.devcontainer/devcontainer.json` if present (subset):

```json
{
  "image": "ubuntu:22.04",
  "postCreateCommand": "npm install",
  "forwardPorts": [3000, 5432],
  "dotfiles": { "repository": "https://github.com/you/dotfiles" }
}
```

devd also works with [devenv](https://devenv.sh). The two complement each other: devd orchestrates the VM, devenv configures what's inside.

## Images

devd doesn't care what runs inside the VM. Bring any OCI image:

```bash
devd new myapp                          # default: alpine + nix
devd new myapp --image ubuntu:22.04     # your choice
devd new myapp --image ghcr.io/myorg/devbox:latest
```

A default image (Alpine + Nix, ~50MB base) is provided for convenience.

## License

Apache-2.0