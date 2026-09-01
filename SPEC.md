# devd — Design Specification

## Problem

Development environments are heavy, slow, and machine-bound. On macOS, every container solution (Docker Desktop, Podman, Lima, Colima) follows the same pattern: run a persistent Linux VM, then run containers inside it. This means double-layered virtualization, gigabytes of background RAM usage, and shared-kernel isolation between workspaces.

Moving work between machines means rebuilding state from scratch. There is no standard way to snapshot a development environment and restore it elsewhere.

## Core Insight

Use microVMs directly — one per workspace — instead of running containers inside a hidden VM. Separate orchestration (devd) from environment definition (devenv/devcontainer). Enable cold migration via filesystem snapshots.

---

## Architecture

```
┌──────────────────────────────────────────────┐
│  devd CLI + daemon (Go binary)               │
│  ────────────────────────────────────────    │
│  • Shell out to devd-vm (VM lifecycle)       │
│  • SSH config + CA manager                   │
│  • Automatic declared-port proxy             │
│  • SQLite (workspace metadata)               │
│  • S3 client (v0.3+)                         │
└───────┬──────────────────────────────────────┘
        │
        ├── manages ──► SQLite (~/.devd/devd.db)
        │
        ├── shells out ──► devd-vm + libkrun
        │                    │
        │               ┌────┴─────┐
        │               │  microVM │ ×N (one per workspace)
        │               │  ──────  │
        │               │  sshd    │◄── SSH tunnels (port relay)
        │               │  server  │    + IDE integration
        │               │  :8080   │
        │               └──────────┘
        │
        └── pre-empts ──► 0.0.0.0:<declared ports>
                          (blocks TSI, proxies to active VM)
```

devd shells out to its separately linked `devd-vm` runtime companion for VM lifecycle. The Go binary remains CGO-free. On macOS, libkrun uses Apple's Hypervisor.framework; on Linux, it uses KVM. There is no intermediate VM layer. Each workspace is a first-class microVM whose writable root is one raw ext4 disk.

**Networking** uses libkrun's TSI (Transparent Socket Impersonation). Undeclared guest ports are exposed automatically. Declared host-facing ports are always pre-empted by an automatically managed devd proxy before the VM starts. This gives stable semantics if another workspace later declares the same port, forces TSI to fall back to real guest sockets, and preserves guest loopback. The proxy reaches the selected guest through OpenSSH stream-local forwarding.

---

## Component Stack

| Layer | Component | Role |
|-------|-----------|------|
| CLI | [cobra](https://github.com/spf13/cobra) | Command parsing |
| VM runtime | `devd-vm` (shelled out) | Thin separately linked libkrun companion; Go remains no-CGO |
| Guest root | raw ext4 over virtio-blk | One writable cloneable disk per workspace |
| Host mount | virtio-fs (built into libkrun) | Host ↔ VM code mount at `/workspace` |
| Networking | libkrun TSI | Transparent socket proxying via vsock |
| Port proxy | automatically managed devd process | Pre-empts declared ports and routes through SSH stream-local tunnels |
| SSH | Shared Ed25519 keypair | IDE integration, SSH tunnels, `~/.ssh/config` management |
| State | [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) | Workspace metadata, pure Go, no CGO |
| Base image | OCI image with current guest prerequisites | The ext4 converter is image-agnostic; v0.1 boot currently requires OpenSSH server utilities in the image |
| IDE integration | SSH | VS Code Remote-SSH, Cursor, any SSH-capable editor |
| Env config | [devenv](https://devenv.sh) / devcontainer.json (subset) | User-defined environment |

---

## Key Design Decisions

### Direct microVM, not container-in-VM

On macOS, Docker Desktop and Podman both run a persistent Linux VM and execute containers inside it. This is because containers depend on Linux kernel primitives (namespaces, cgroups) that macOS does not provide.

libkrun sidesteps this by creating microVMs that boot their own Linux kernel directly on the host hypervisor. Each workspace gets its own kernel, its own userspace, and full isolation — without an intermediate VM layer.

Trade-off: we lose Docker API compatibility and the VS Code Dev Containers plugin. We gain single-layer virtualization, per-workspace kernel isolation, zero background overhead, and sub-second boot times.

### SSH for IDE integration, not Docker API

The VS Code Dev Containers extension requires a Docker-compatible socket. Implementing a Docker API shim is significant engineering for marginal benefit. Instead, devd runs sshd inside each microVM and manages `~/.ssh/config` entries automatically.

VS Code Remote-SSH is more mature, works with every editor (Cursor, JetBrains Gateway, Neovim + ssh), and doesn't couple us to Docker's API surface. Port forwarding, terminal access, and extension installation all work over SSH.

### SSH keypair for workspace access

devd generates a single Ed25519 keypair on first run (`~/.devd/ssh/devd_ed25519`). The public key is injected into each workspace's root authorized_keys at VM creation time. The user's `~/.ssh/config` is updated with a `Host devd-<name>` entry pointing to the right port and key, so `ssh devd-myapp` just works — no manual key acceptance, no known_hosts conflicts.

A CA-based model (per-VM host certificates) is a possible future improvement for fleet-scale use, but the shared keypair is sufficient for local development.

### Shell out, not CGO

The Go CLI never links libkrun. It shells out to the separately built and signed `devd-vm` companion, which exposes only the root-disk and one-time conversion operations devd needs. A static Linux `devd-image-helper` performs OCI-to-ext4 copying in a disposable helper VM. The installed bundle is therefore three executables while the main Go binary remains simple, pure Go, and cross-compilable.

krunvm's Buildah-VFS workspace lifecycle is not used. Directory-root mode exists only inside the cold converter so logical OCI ownership, modes, links, xattrs, and capabilities can be recorded into ext4. Every user workspace is ext4-only.

### Digest-addressed ext4 image cache

devd's storage pipeline accepts OCI images and never creates a Buildah container per workspace. On first use it resolves the local image to an immutable digest, exposes that rootfs read-only to a pinned helper VM, and copies Linux metadata into a sparse ext4 image. The completed template is checked, fsynced, and atomically published under:

```text
~/.devd/images/sha256-<digest>-<arch>-v<format>-<size>/rootfs.ext4
```

A workspace is one copy-on-write clone at `~/.devd/workspaces/<name>/rootfs.ext4`. macOS requires APFS clonefile and Linux requires a successful reflink; devd does not silently fall back to a full hot-path copy. Templates are immutable and keyed by digest, architecture, disk format version, and logical disk size.

The default sparse capacity is 32 GiB. Physical host space is consumed only by template data and blocks changed by each clone.

### OCI storage, explicit guest prerequisites

devd resolves OCI images through Buildah, then boots only their cached ext4
representation. The current injected init invokes `sshd`, `ssh-keygen`, and
basic shell utilities from the image, so storage compatibility is broader than
boot compatibility. A general-purpose published default and an
image-independent guest agent are required before claiming arbitrary-image
support.

### Disk fork and cold migration only

A stopped workspace forks by reflink-cloning its single ext4 file. The child preserves complete guest disk state but receives fresh ports, machine ID, and SSH host keys on first boot. Host-mounted project files are outside the disk and are reused or explicitly overridden; they are never implicitly copied.

Live migration adds enormous complexity for dev workloads that do not need it. Portable migration exports the ext4 disk plus workspace JSON rather than serializing a host directory tree.

---

## Networking

### How TSI Works

TSI (Transparent Socket Impersonation) is libkrun's networking layer. The custom libkrunfw kernel intercepts AF_INET socket syscalls inside the guest, converts them to AF_VSOCK messages, and the VMM process on the host proxies them as real AF_INET sockets.

Key behaviors (empirically verified, see `experiments/`):

| Behavior | Details |
|----------|---------|
| **Auto-expose** | Guest `bind(0.0.0.0:8080)` → VMM binds `0.0.0.0:8080` on host. Zero config. |
| **Guest loopback** | `curl localhost:8080` inside the VM reaches the VM's own server (when no host contention). |
| **Pre-emption** | If host already holds `0.0.0.0:X` (no `SO_REUSEPORT`), TSI's bind fails silently. Guest `bind()` still succeeds — guest kernel falls back to real socket handling. Loopback is truly local. `ss -tlnp` shows real listeners. |
| **Multi-VM coexistence** | Multiple VMs can bind the same port. Kernel routes to one; survivors take over when others release. |
| **No port maps** | `krun_set_port_map` breaks guest loopback. devd must not use it. |

### Port Pre-emption and the Proxy Architecture

Every port declared with `--ports` is managed from the first workspace onward:

1. `run`, `start`, or `fork` asks the local proxy process to bind
   `0.0.0.0:<declared-port>` before starting the VM. The process and its Unix
   control socket are started automatically.
2. TSI's host-side bind fails for that port, so the guest uses a real kernel
   socket and guest loopback remains local.
3. The host listener selects the active running workspace that declares the
   port. A sole running claimant is selected even if another workspace is
   globally active.
4. On first traffic, the proxy creates an OpenSSH `-L` stream-local tunnel:
   a short host Unix socket forwards to the guest's `127.0.0.1:<port>`.
5. `devd switch` changes the database routing decision. Existing VMs, guest
   listeners, and established connections are not restarted.
6. Removing the last port declaration shuts down the proxy process. Undeclared
   guest ports continue to use transparent TSI exposure.

Pre-empting the first declaration, rather than waiting until a port becomes
contested, removes the impossible transition where a running VM already owns
the host port when a second workspace claims it. It also removes all manual
daemon ordering from the user-facing contract.

### devd switch

`devd switch frontend` marks `frontend` active. For each shared declared port it
claims, the next host connection routes through frontend's SSH tunnel. Ports
with one running claimant continue routing to that claimant.

**Properties:**
- **Zero disruption.** Nothing is killed or restarted inside any VM.
- **Fast.** Existing tunnels switch on the next connection; the first connection
  to a workspace may pay one SSH handshake.
- **Fully reversible.** A→B→A→B switching does not mutate guest state.
- **Guest isolation.** Every participating VM keeps independent guest loopback.

### Platform Differences

| Aspect | macOS | Linux |
|--------|-------|-------|
| Hypervisor | Hypervisor.framework | KVM |
| VM runtime | libkrun via `devd-vm` | libkrun via `devd-vm` |
| Networking | TSI | TSI (or pasta — has real netns, loopback works unconditionally) |
| Port proxy | Same on both | Same on both |

---

## Environment Configuration

Two paths. devd orchestrates the VM; the user configures what's inside.

**devenv** (primary): User has `devenv.nix` in their project. Inside the VM, `devenv up` starts services via process-compose.

**devcontainer.json** (compatibility, current subset):

| Field | Behavior |
|-------|----------|
| `image` | Used when `run` has no image argument |
| `forwardPorts` | Added to the workspace's automatically managed ports |
| `postCreateCommand` | Parsed, but currently emits an explicit unsupported warning |

JSONC comments and trailing commas are accepted. Lifecycle hooks, dotfiles,
features, Docker Compose, and customizations remain planned rather than being
silently approximated with incorrect semantics.

---

## State: SQLite

SQLite tracks workspace metadata and process state. The workspace ext4 file owns persistent guest state; PID liveness owns actual running state.

```sql
CREATE TABLE workspaces (
    name          TEXT PRIMARY KEY,
    image         TEXT NOT NULL,
    workspace_dir TEXT NOT NULL,
    disk_path      TEXT NOT NULL,
    image_digest   TEXT NOT NULL,
    parent_name    TEXT NOT NULL DEFAULT '',
    ssh_port      INTEGER NOT NULL UNIQUE,   -- unique sshd port per VM
    relay_port    INTEGER NOT NULL UNIQUE,    -- legacy v0.1 allocation; no longer used for routing
    state         TEXT DEFAULT 'stopped',     -- stopped | running
    is_active     BOOLEAN DEFAULT FALSE,      -- selected target for shared declared ports
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE reserved_ports (
    workspace  TEXT REFERENCES workspaces(name),
    port       INTEGER NOT NULL,
    PRIMARY KEY (workspace, port)
);

-- v0.2+
CREATE TABLE snapshots (
    workspace   TEXT REFERENCES workspaces(name),
    path        TEXT NOT NULL,
    metadata    TEXT,               -- JSON: workspace config sidecar
    size_bytes  INTEGER,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

Reserved ports are declared in the workspace config (via `forwardPorts` in devcontainer.json, or devd CLI flags). All declared ports are pre-empted before VM start and routed by the automatic proxy; shared ports follow the active workspace.

---

## Phasing

| Version | Scope | Success Criteria |
|---------|-------|------------------|
| **v0.1** | Ext4 lifecycle, fork, and switch | `devd run nicolaka/netshoot -n myapp && devd ssh myapp` works; `devd fork <stopped-parent> -n child` preserves disk state, creates fresh identity, and boots the child; the default file mount is bidirectional; automatic routing and `devd switch` handle shared declared ports. |
| **v0.2** | Portable snapshots | export/import the ext4 disk plus JSON sidecar. |
| **v0.3** | Remote storage | `devd snapshot --to s3://...` works. Restore on a fresh machine from S3. |
| **v0.4** | Remote nodes | `devd new --remote <server>`. Server runs devd in agent mode. SSH/mTLS control plane. |
| **v0.5** | Migration | `devd move myapp --to <server>`. Snapshot → upload → restore. |
| **v0.6+** | Polish | VS Code/Cursor extension for one-click connect. devenv.yaml generation. OCI image export. |

### v0.1 Scope Boundaries

**In scope**: Workspace lifecycle (run/start/stop/fork/rm), automatic proxy-based port routing and `devd switch`, microVM per workspace, SSH access (shared keypair), and OCI image selection for images containing the current OpenSSH guest prerequisites.

**v0.1.2**: devcontainer.json subset (postCreateCommand, forwardPorts, dotfiles).

**Out of scope**: Remote nodes, live migration, full devcontainer spec, IDE plugins, image building, dynamic port detection.

---

## Why Not X?

### Docker Desktop

Runs a LinuxKit VM (~2-4GB RAM) permanently. All containers share that VM and its kernel. No per-workspace isolation. No snapshot/migration. Proprietary license for enterprise. devd replaces the VM-in-VM architecture with direct microVMs.

### Podman

Better than Docker Desktop (daemonless, rootless, libkrun machine provider on Mac). But on macOS, `podman machine` still runs a Fedora CoreOS VM, and all containers execute inside it. Using `--runtime krun` for individual containers means microVMs inside a VM — double nesting. devd eliminates the outer VM.

### Dev Containers (spec)

The devcontainer spec assumes Docker/Podman as the runtime. The VS Code plugin uses `docker exec` for communication. Supporting the full spec requires implementing a Docker API shim. devd supports the useful subset (postCreateCommand, forwardPorts, dotfiles) and uses SSH instead of Docker for IDE integration.

### Apple Containerization (macOS 26+)

Apple's new `container` tool runs each container in its own lightweight VM — architecturally similar to devd. However, it's Swift-only, macOS-only (requires macOS 26 Tahoe), and has no snapshot/migration story. devd works on both macOS and Linux using the same codebase, and is designed for workspace mobility from day one.

### Lima / Colima

Convenience wrappers around QEMU or Virtualization.framework. Still a single VM that hosts containers. Same double-layer problem.

---

## Open Questions

1. **libkrun pause/resume**: libkrun currently has no pause/suspend API. If upstream adds this, it could complement the proxy switch by reducing idle VM resource usage.
2. **pasta on Linux**: pasta provides a real network namespace — guest loopback works unconditionally and port detection is built in. Should Linux use pasta instead of TSI? Proxy architecture still works either way.

---

## Appendix: TSI Internals

For contributors working on the networking layer. Based on libkrun source analysis and six rounds of empirical testing. See `experiments/` for scripts and detailed results.

### How TSI intercepts sockets

TSI operates at the guest kernel level via a custom kernel module in libkrunfw. When a guest process calls `socket()`, `bind()`, `connect()`, `listen()`, or `accept()` on AF_INET sockets, the kernel module intercepts these and communicates with the VMM via virtio-vsock control messages.

Key data structures (from libkrun `src/devices/src/virtio/vsock/`):

- `TsiConnectReq { peer_port, addr, port }` — guest initiates outbound connection
- `TsiListenReq { peer_port, addr, port, vm_port, backlog }` — guest calls bind+listen
- `TsiAcceptReq` — guest polls for incoming connections
- Host port map: `Option<HashMap<u16, u16>>` — if `Some`, only mapped ports can bind; unmapped returns `EPERM`. If `None`, transparent 1:1 binding.

### Port map behavior

```rust
// From tcp.rs try_listen()
let port = if let Some(port_map) = host_port_map {
    if let Some(port) = port_map.get(&req.port) {
        *port           // remap to host port
    } else {
        return -EPERM;  // deny: port not in map
    }
} else {
    req.port            // transparent: same port on host
};
// Then: bind(host, 0.0.0.0, port)
```

devd MUST NOT set a port map. Doing so breaks guest loopback and restricts which ports can bind inside the guest.

### Guest loopback — two modes

**Normal (TSI active):** Guest `connect(127.0.0.1:X)` is proxied by TSI to the host, where it connects to `127.0.0.1:X` — which resolves to the same VM's `*:X` listener. Loopback works but is indirect. `ss -tlnp` shows nothing because TSI intercepts below the kernel socket table.

**Fallback (TSI host-side bind fails):** When the host already holds `0.0.0.0:X` (without `SO_REUSEPORT`), TSI's `bind(0.0.0.0:X)` on the host fails. The guest's `bind()` still returns success, but the guest kernel falls back to real socket handling. `ss -tlnp` shows the listener. Guest loopback is truly local — not proxied through TSI.

This fallback is the foundation of devd's proxy architecture.

### Full empirical behavior table

Verified on macOS ARM64, krunvm 0.2.6, Feb 2026. Six rounds of testing across Experiments 1–6.

| Scenario | Result |
|----------|--------|
| Guest binds `:8080`, no contention | VMM binds `0.0.0.0:8080` on host. Auto-exposed. |
| Guest `curl localhost:8080` (no contention) | Works. TSI routes to own server. |
| Host holds `0.0.0.0:8080` (no `SO_REUSEPORT`) before VM | TSI host-side bind blocked. Guest falls back to real sockets. |
| Guest `bind()` after `0.0.0.0` pre-emption | Succeeds (`strace`: `bind() = 0`). |
| Guest loopback after pre-emption | Works — truly local. `ss -tlnp` shows real listener. |
| Host curl after pre-emption | Hits host daemon only (10/10). |
| SSH tunnel relay (proxy → `ssh -L` → VM) | Works end-to-end. |
| Two-VM proxy switch | Instant (~20ms). Fully reversible. Both VMs have loopback. |
| Host holds `127.0.0.1:8080`, TSI holds `0.0.0.0:8080` | Coexist. Host shadows TSI for localhost callers. Guest loopback breaks. |
| Host uses `SO_REUSEPORT` on `0.0.0.0:8080` | TSI coexists — connections distributed unpredictably. Don't use. |
| Two VMs both bind `:8080` (no pre-emption) | Both succeed. First-to-bind wins deterministically. |
| Kill one VM | Surviving VM takes over (~25ms). |
| SIGSTOP on VMM process | No failover. Connections queue and timeout. |
| `krun_set_port_map` | Breaks guest loopback. |
| `ss -tlnp` under normal TSI | Always empty. |

**Why pre-emption is necessary for multi-VM:** Without pre-emption, TSI routes all socket operations through the host. When two VMs bind the same port, guest loopback is not isolated — VM-B's `curl localhost:8080` goes through TSI to the host and hits whichever VM the kernel considers the port owner (VM-A), not VM-B's own server. Pre-emption forces guests onto real kernel sockets, making loopback truly local and independent per VM.
