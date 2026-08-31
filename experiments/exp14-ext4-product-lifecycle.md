# Experiment 14: Product ext4 Lifecycle and Fork

**Status:** PASS on macOS ARM64 (2026-08-31)  
**Platform:** Apple M5 / macOS 26.5.1 / APFS  
**Runtime:** bundled `devd-vm`, libkrun 1.19.4  
**Image:** `docker.io/nicolaka/netshoot`

## Decision being tested

Experiments 12 and 13 validated the ext4 design with experiment-only launchers.
Experiment 14 validates the actual product path after removing krunvm workspace
storage:

```text
OCI image → digest template.ext4 → source/rootfs.ext4
                                      │ stopped reflink clone
                                      ▼
                                  child/rootfs.ext4 → boot
```

The public fork operation is creation-and-start, not a separate stopped-fork
command:

```bash
devd stop source
devd fork source --name child
```

## Method

Script: [`exp14-ext4-product-lifecycle.sh`](./exp14-ext4-product-lifecycle.sh)

1. Build the three-binary product bundle.
2. Use an isolated devd state directory and SSH config.
3. Run a source workspace from netshoot with a host `/workspace` mount.
4. Write persistent state under `/root` and record machine/SSH identity.
5. Stop the source cleanly.
6. Fork and start a child with `devd fork source --name child`.
7. Verify inherited guest state, inherited host-mount configuration, fresh
   machine ID, and fresh SSH host key.
8. Write child-only disk state, stop the child, restart the source, and verify
   write isolation.
9. Remove both workspaces.

The product converter uses the static `devd-image-helper` as a pinned helper
root. It does not rely on `cp`, `mount`, or other tools from the user's OCI
image. The OCI rootfs is exposed read-only through virtio-fs and copied into
ext4 with Linux ownership, mode, hardlink, symlink, timestamp, xattr, and device
metadata handling.

## Acceptance criteria

| Check | Required |
|---|---|
| User workspace backend | Raw ext4 only |
| Source state | Source must be stopped |
| Fork implementation | Reflink/clonefile; no full-copy fallback |
| Child state | Source guest disk state inherited |
| Child lifecycle | Running and SSH-ready when `fork` returns |
| Machine ID and SSH host keys | Different from source |
| Host project mount | Reused by default; host files not copied |
| Child disk writes | Not visible after source restart |
| Clean stop/restart | State and ext4 remain valid |

## Results

The first production run prepared a 32 GiB sparse netshoot template in 10.25s,
cloned the source disk in 23ms, and reached SSH in 0.40s. This cold conversion
is paid once per digest.

The fork run produced:

| Metric | Result |
|---|---:|
| Source disk → child disk clone | **16ms** |
| Child boot → SSH | **0.20s** |
| `fork` create-to-SSH | **0.22s** |
| Persistent source state inherited | PASS |
| Machine identity divergence | PASS |
| SSH host-key divergence | PASS |
| Host `/workspace` reused | PASS |
| Child write isolation | PASS |
| Graceful source/child stop and restart | PASS |

## Conclusion

**PASS.** The ext4 architecture is now the product lifecycle rather than an
experimental alternate path. OCI images pay one digest-addressed conversion;
normal workspace creation and stopped-workspace branching are single-file
copy-on-write clones. `devd fork source --name child` returns with the child
running and independently identified in roughly 220ms on the test host.
