# Experiment 12: One ext4 Disk Image per Workspace

**Status:** PASS on macOS ARM64 (2026-08-30)  
**Platform:** macOS ARM64 / APFS  
**Runtime:** libkrun through the experimental `devd-vm` launcher  
**Default image:** `nicolaka/netshoot`  
**Depends on:** Experiments 8–11

## Decision being tested

Replace the current per-workspace Buildah VFS directory with one writable raw
ext4 image per workspace.

An OCI image is converted to an immutable ext4 template once. Creating or
forking a workspace APFS-clones that single file, then boots the clone as the
VM's root block device.

```text
OCI image
   │ one-time pull + extraction
   ▼
Buildah merged rootfs directory
   │ one-time helper-VM copy
   ▼
immutable template.ext4
   │ APFS clonefile (cp -c)
   ├──────────────► workspace-a/rootfs.ext4
   └──────────────► workspace-b/rootfs.ext4
```

This is a disk fork, not a memory fork. Each VM boots normally and does not
inherit running processes.

## Why this is the next experiment

Experiments 8 and 9 measured:

- `krunvm create`: 8–12 seconds;
- VM boot to SSH: 0.6–0.9 seconds.

Experiment 10 isolated the create cost to Buildah's VFS directory copy on
macOS. Experiment 11 showed that bypassing Buildah while retaining a directory
rootfs only improves netshoot from roughly 8 seconds to 4–6 seconds because
APFS still performs one clone operation per file.

The same experiments measured a single-file APFS clone at 9–15 ms. A raw ext4
rootfs makes that primitive usable for complete workspace state.

## Hypothesis

For a cached OCI image on APFS:

1. The cold conversion to ext4 may remain slow, but is paid once per image
   digest.
2. Each workspace disk clone takes less than 100 ms regardless of the number of
   files in the image.
3. Two clones boot concurrently and reach SSH in less than 2 seconds.
4. Writes, machine identity, and SSH host keys diverge between clones.
5. A clone survives both graceful guest shutdown and the current devd-style
   abrupt VMM termination without filesystem corruption.
6. Copying the OCI rootfs from inside a helper VM preserves Linux ownership,
   mode bits, hardlinks, symlinks, xattrs, and file capabilities.

If these hold, the experiment's storage lifecycle—not its C launcher—is the
implementation blueprint for production.

## Why populate ext4 from inside a helper VM

Buildah's rootless macOS VFS store represents Linux metadata using container
storage xattrs such as `user.containers.override_stat`. Running host-side
`mke2fs -d <rootfs>` risks copying the visible macOS UID/GID/mode instead of the
logical OCI metadata.

The experiment therefore:

1. creates an empty ext4 filesystem with host `mke2fs`;
2. boots the Buildah directory root through libkrun/virtio-fs;
3. attaches the empty ext4 image as `/dev/vda`;
4. copies the rootfs from inside Linux using `cp -a`;
5. unmounts and shuts down the helper.

libkrun's virtio-fs layer presents Linux metadata to the helper guest, and ext4
records what the guest sees. The helper also creates a synthetic metadata
fixture with non-root ownership, setuid mode, hardlink, symlink, and—when the
image has the required tools—an xattr and file capability. Children verify the
fixture after boot.

A production implementation should use a small, pinned devd helper image rather
than relying on tools from the user's image.

## Method

Script: [`exp12-ext4-disk-fork.sh`](./exp12-ext4-disk-fork.sh)

### Phase 1: Build the experiment-only launcher

Compile and ad-hoc-sign `experiments/devd-vm-prototype/main.c`. Experiment 12
adds `--data-disk` for the conversion helper. The launcher supports:

- directory root + target data disk during conversion;
- ext4 root disk when booting workspace clones.

This removes the current krunvm CLI's lack of root-disk support as an
experimental confounder. It is not approval to link libkrun into the production
Go binary.

### Phase 2: Prepare one ext4 template

1. `buildah from --os linux --arch arm64 <image>`.
2. Mount the temporary merged rootfs.
3. Remove per-workspace identity:
   - SSH host keys;
   - `/etc/machine-id` contents.
4. Add a generic experiment init script.
5. Create a sparse ext4 file with `mke2fs`.
6. Boot a helper VM with the Buildah directory as root and ext4 as `/dev/vda`.
7. Copy the root tree to ext4 and unmount it.
8. Remove the temporary Buildah working container.

Record the complete cold preparation time, but do not apply it to the hot-path
acceptance threshold.

### Phase 3: Fork two workspace disks

Use macOS `/bin/cp -c` twice:

```bash
/bin/cp -c template.ext4 child-a.ext4
/bin/cp -c template.ext4 child-b.ext4
```

Record clone latency and APFS free-space delta. The free-space measurement is
informational because unrelated filesystem activity can affect it; successful
`cp -c` and latency are the primary proof that clonefile was used.

### Phase 4: Boot concurrently

Boot both ext4 roots with:

- separate VMM processes;
- separate SSH ports;
- per-instance name and port passed as init environment;
- no memory snapshot/restore.

Record wall time from launch to SSH readiness.

### Phase 5: Correctness and isolation

Verify:

- `/bin/sh` remains owned by root and executable;
- synthetic ownership, setuid, hardlink, symlink, xattr, and capability metadata;
- distinct non-empty machine IDs;
- distinct SSH host keys;
- child A and B can write different `/fork-marker` contents;
- a 16 MiB write in A is not visible in B.

### Phase 6: Persistence and crash behavior

1. Terminate A's main workload after `sync`, allowing `init.krun` to unmount
   the block root and shut down the guest cleanly;
2. terminate B's VMM with `SIGTERM`, matching current `devd stop` behavior;
3. restart both from the same images;
4. verify markers and identities persisted;
5. shut down both gracefully;
6. run read-only `e2fsck` on both images.

## Acceptance criteria

All are blocking unless marked informational:

| Check | Required result |
|---|---|
| APFS clone A | <100 ms |
| APFS clone B | <100 ms |
| Concurrent boot A → SSH | <2 seconds |
| Concurrent boot B → SSH | <2 seconds |
| Root and fixture metadata | Preserved |
| Machine IDs | Distinct and stable across restart |
| SSH host keys | Distinct and stable across restart |
| Child writes | Isolated |
| Graceful restart | State preserved |
| Abrupt VMM restart | Journal recovers; state preserved |
| Final `e2fsck -fn` | Clean |
| APFS free-space delta | Record only |
| Cold template conversion | Record only |

The performance target for productization is stricter after correctness is
established: **p50 clone <25 ms and p95 create-to-SSH <1 second over 20 runs**.

## Running it

`mke2fs` and `e2fsck` are needed to create and inspect ext4 without mounting it
on macOS. They are included in the project devenv, so after `direnv reload` run:

```bash
bash experiments/exp12-ext4-disk-fork.sh docker.io/nicolaka/netshoot
```

Outside the project devenv, use `nix shell nixpkgs#e2fsprogs` first.

The script also accepts `nicolaka/netshoot` and qualifies Docker Hub shorthand
automatically. This is necessary on hosts whose containers configuration has no
`unqualified-search-registries` entry. It also locates Homebrew's containers
signature policy explicitly because Homebrew installs it under
`/opt/homebrew/etc/containers/policy.json` while Buildah otherwise looks under
`/etc/containers`. Override discovery with `SIGNATURE_POLICY=/path/policy.json`.
The experiment launcher is linked with Homebrew libkrunfw's directory as an
rpath because libkrun loads `libkrunfw.5.dylib` dynamically; override that
discovery with `LIBKRUNFW_PREFIX=/path/to/libkrunfw`.

Keep logs and disk images for inspection:

```bash
KEEP=1 bash experiments/exp12-ext4-disk-fork.sh
```

The script is destructive only inside its temporary Buildah working container
and experiment work directory. It does not modify devd's SQLite database.

## Target production state

### On-disk layout

```text
~/.devd/
├── images/
│   └── sha256-<oci-manifest-digest>/
│       ├── rootfs.ext4       # immutable template; never booted writable
│       └── manifest.json     # OCI/runtime/config/cache metadata
└── workspaces/
    └── <name>/
        ├── rootfs.ext4       # writable APFS clone owned by this workspace
        ├── config.json       # instance settings and template lineage
        └── vm.log
```

The image-cache key must include at least:

- canonical OCI manifest digest;
- OS and architecture;
- disk format version;
- helper/init version;
- libkrun/libkrunfw compatibility version.

The template is written to a temporary path, checked, fsynced, and atomically
renamed. A per-key lock prevents duplicate concurrent conversion.

### Database model

Do not repurpose the current `rootfs_dir` column: it currently points at devd's
workspace metadata directory, not the krunvm rootfs. Add explicit fields:

```text
disk_path       TEXT
image_digest    TEXT
storage_kind    TEXT   # legacy-krunvm-vfs | ext4-raw
parent_name     TEXT   # nullable; lineage for devd fork
```

Keep legacy workspaces readable during rollout. New workspaces use ext4 after
the feature gate is enabled; an explicit migration can come later.

### Runtime contract

The Go binary remains CGO-free and shells out. Production needs a supported
runtime command capable of:

```text
start --root-disk <path> --cpus N --mem M --env ... --mount ...
```

Preferred route: add raw root-disk support to krunvm and consume it from
`internal/vm/krunvm.go`. The experiment's C wrapper is disposable proof code.
If upstream krunvm will not take the feature, adopting a separately shipped
helper requires an explicit SPEC change and packaging decision.

The runtime must also support graceful guest shutdown. `SIGTERM` remains a
bounded fallback, but should not be the normal stop path for writable block
images.

## Productization work breakdown

### Milestone 0 — validate Experiment 12

- Run on netshoot and the planned small default image.
- Run 20 clone/boot iterations after the first correctness pass.
- Profile cold conversion separately from clone and boot.
- Inspect failures in metadata, journal recovery, and TSI.

**Exit:** every blocking acceptance check passes.

### Milestone 1 — storage primitives

Add a small storage package responsible only for:

- immutable template paths and locking;
- sparse ext4 allocation;
- APFS clonefile with explicit failure (no silent full copy on the hot path);
- atomic temp-file cleanup;
- disk validation and optional resize;
- source/destination same-volume checks.

On Linux, retain the existing overlay-backed krunvm path initially. Raw-disk
fork can later use reflink-capable filesystems or qcow2 overlays; do not make a
full ext4-host copy the Linux fast path.

### Milestone 2 — image preparation/cache

- Resolve image references to immutable digests.
- Pull once and create one temporary Buildah container.
- Run a pinned helper VM to copy OCI state into ext4.
- Preserve OCI environment, workdir, entrypoint/cmd in `manifest.json`.
- Sanitize machine identity and install a versioned generic devd init.
- Verify and atomically publish the template.
- Add `devd pull`/prepare behavior or invoke preparation transparently from
  create.

### Milestone 3 — root-disk runtime

- Add/upstream krunvm root-disk start support.
- Reproduce current CPU, memory, log, TSI, and virtio-fs mount behavior.
- Pass per-instance SSH/name/command configuration without modifying the
  immutable template.
- Implement graceful shutdown with timeout and VMM-kill fallback.

### Milestone 4 — create lifecycle

Change `doCreate()` to:

1. resolve and ensure the image template;
2. allocate DB ports and workspace directory;
3. APFS-clone `rootfs.ext4` to a temporary workspace path;
4. write instance config;
5. atomically publish the disk and DB record;
6. start through the normal existing start path when invoked by `run`.

`rm` removes only the workspace clone. Image templates get separate reference
tracking/GC.

### Milestone 5 — stopped workspace fork

Add:

```bash
devd fork <source> --name <destination>
```

Initial semantics:

- source must be stopped;
- clone the source's ext4 image, not the original OCI template;
- copy CPU/memory/reserved-port configuration;
- allocate new SSH and relay ports;
- preserve rootfs state;
- preserve or override the host `/workspace` mount explicitly;
- generate new machine and SSH host identity on destination's first boot.

The source's host-mounted project directory is not automatically copied. The
CLI must state whether it reuses the mount or requires `--mount` for an
independent checkout.

### Milestone 6 — rollout and cleanup

- Keep `legacy-krunvm-vfs` support for existing records.
- Add cache inspection and GC.
- Benchmark create/fork in CI where APFS/HVF hardware is available.
- Update SPEC, README performance numbers, and operational recovery docs.
- Remove the directory-root create path only after an explicit migration or a
  compatibility window.

## Risks the experiment must expose

1. **Metadata loss:** rootless Buildah metadata may not survive the helper copy.
2. **Disk-root boot assumptions:** the installed libkrun root-disk API is
   deprecated and may differ from the API product code ultimately uses.
3. **Image tools:** arbitrary OCI images may not contain a sufficiently capable
   `cp`; production needs a pinned helper environment.
4. **Fixed capacity:** raw ext4 has a maximum size even when sparse. A default
   and resize policy are required.
5. **Stop semantics:** abrupt VMM termination relies on ext4 journal recovery.
6. **Mount semantics:** host virtio-fs project mounts remain outside the forked
   disk.
7. **Port ordering:** a fork can create new contested ports. Existing daemon
   pre-emption rules still apply before either fork is started.

## Results

Validated on Apple Silicon with libkrun 1.19.4, libkrunfw 5, a cached
`docker.io/nicolaka/netshoot:latest`, and a 2 GiB sparse ext4 image.

| Metric | Result |
|---|---:|
| Cold cached OCI → ext4 preparation | 9,109 ms |
| APFS clone A | 16 ms |
| APFS clone B | 15 ms |
| APFS space consumed by both initial clones | 8 KiB (informational) |
| Concurrent boot A → SSH | 417 ms |
| Concurrent boot B → SSH | 450 ms |
| Metadata fixture | PASS |
| Identity divergence and restart stability | PASS |
| Write isolation | PASS |
| Abrupt-stop journal recovery | PASS |
| Final `e2fsck -fn` | PASS |

The first run exposed two runtime details now encoded in the experiment:

1. A block root must use `krun_add_disk(..., "root", ...)` followed by
   `krun_set_root_disk_remount(..., "/dev/vda", "ext4", NULL)`. The deprecated
   `krun_set_root_disk` attaches the disk but does not arrange the dummy
   virtio-fs root needed to run libkrun's built-in init before pivoting.
2. A clean shutdown is triggered by syncing and terminating the main workload,
   allowing `init.krun` to unmount the block root. The image's `poweroff` command
   does not control `init.krun` in this setup.

## Conclusion

The experiment passes its blocking criteria and validates the ext4 storage
lifecycle for productization. A cached workspace can be cloned in 15–16 ms and
reach SSH in 417–450 ms, while retaining isolated, persistent ext4 state.

This authorizes implementing the storage/cache milestones and pursuing krunvm
root-disk support. It does not authorize live memory fork, CGO, or direct
libkrun linkage in the Go binary.
