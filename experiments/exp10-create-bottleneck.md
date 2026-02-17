# Experiment 10: Why `krunvm create` Takes 8–12 Seconds

**Date:** 2026-02-17  
**krunvm version:** 0.2.6  
**buildah version:** 1.28.0  
**Platform:** macOS ARM64 (APFS filesystem)  
**Depends on:** Experiments 8–9 (established create as the bottleneck)

## Question

Experiments 8 and 9 showed `krunvm create` takes ~10–12s while VM boot is sub-second. Why? What can we do about it?

## Background

`krunvm create` does three things:
1. Calls `buildah from <image>` — creates a new buildah container from the OCI image
2. Mounts the container, writes `.krun_config.json` and `/etc/resolv.conf`
3. Stores VM config in a TOML file (`~/Library/Preferences/rs.krunvm/krunvm.toml`)

Steps 2–3 take milliseconds. Step 1 is the bottleneck.

### Why `buildah from` is slow on macOS

buildah's storage driver on macOS is **VFS** (the only option — overlayfs requires Linux). VFS has no copy-on-write. Every `buildah from` creates a **full copy** of the merged rootfs directory. For `nicolaka/netshoot`, that's:

- **633MB** on disk
- **15,097 files** across 2,109 directories
- **15 OCI layers** (VFS maintains cumulative copies at each layer)

On Linux with overlayfs, `buildah from` would be near-instant (just creates an empty upper directory).

## Method

### Phase 1: Isolate the cost

Separately time:
1. `buildah pull` (OCI image download)
2. `krunvm create` with cached image (VFS copy + metadata)
3. `buildah from` directly (VFS copy only)

### Phase 2: Profile alternatives

Time different rootfs cloning methods on the same 633MB/15K-file directory:
1. `cp -R` (regular copy)
2. `cp -Rc` (APFS per-file clone)
3. `tar | tar` (stream copy)
4. `cp -c` on a single 633MB file (APFS clone)

### Phase 3: Test image size impact

Time `krunvm create` across images of different sizes (all cached):
- busybox (~4MB)
- alpine (~8.5MB)
- debian-slim (~75MB)
- netshoot (~633MB)

## Results

### Phase 1: Where the time goes

| Operation | Time | Notes |
|-----------|------|-------|
| `buildah pull` netshoot (network) | ~107s | One-time; image cached after first pull |
| `buildah from` netshoot (cached) | **8,108ms** | VFS full copy of 633MB rootfs |
| `buildah mount` | 35ms | Negligible |
| `buildah unmount` | 27ms | Negligible |
| krunvm overhead (TOML write, etc.) | ~300ms | Negligible |

**100% of the create time is `buildah from` (VFS copy).**

### Phase 2: Rootfs cloning alternatives

All tests on the same 633MB directory (15,097 files, 2,109 dirs):

| Method | Time | Notes |
|--------|------|-------|
| `buildah from` (VFS) | 8,108ms | Current method |
| `cp -R` (regular copy) | 9,792ms | Similar to VFS — raw I/O bound |
| `cp -Rc` (APFS per-file clone) | 3,941ms | ~2x faster, but still 15K syscalls |
| `tar \| tar` (stream) | 16,408ms | Worse — serialization overhead |
| **`cp -c` single 633MB file** | **9ms** | **~1000x faster** — APFS metadata-only |
| `cp -c` single dmg file | 15ms | Same — APFS clone is O(1) |

**Key insight:** APFS can clone a single 633MB file in 9ms. But cloning a directory tree with 15K files still takes ~4s because each file must be individually cloned via `clonefile()` syscall.

### Phase 3: Image size impact

All images pre-cached. Create times include `buildah from` only (no network pull):

| Image | Rootfs Size | Files | `buildah from` | `krunvm create` |
|-------|-------------|-------|-----------------|-----------------|
| alpine | 8.5MB | 83 | 3,288ms | 3,497ms |
| alpine (2nd run) | 8.5MB | 83 | 3,200ms | — |
| busybox | ~4MB | ~50 | — | 4,546ms* |
| debian-slim | ~75MB | ~3K | — | 4,209ms* |
| netshoot | 633MB | 15,097 | 8,108ms | 8,384ms |

\* First run included image pull; re-run values are approximate.

### Cost breakdown

| Component | Time | Notes |
|-----------|------|-------|
| **Fixed overhead** | ~3.2s | buildah VFS storage init, lock, metadata |
| **Per-MB VFS copy** | ~7.8ms/MB | Proportional to rootfs size |
| **netshoot total** | ~8.1s | = 3.2s fixed + 4.9s copy (633MB) |
| **alpine total** | ~3.3s | = 3.2s fixed + 0.1s copy (8.5MB) |

The 3.2s fixed overhead is buildah opening its VFS storage graph (iterating all layers, validating checksums, acquiring locks). Even copying 83 files (8.5MB) takes 3.2 seconds because the overhead is in storage management, not file I/O.

## Analysis

### The fundamental problem

macOS has no overlayfs. buildah's VFS driver is the only option, and it:
1. Makes full copies of the entire merged rootfs for every container
2. Has ~3.2s of fixed storage management overhead per operation
3. Maintains cumulative layer copies (15 layers × 633MB = 8.2GB for one netshoot image)

### Why APFS cloning doesn't fully solve it

APFS `clonefile()` is O(1) for a single file, but there's no directory-level clone primitive. Cloning a directory requires iterating every file:
- 15,097 individual `clonefile()` calls
- 2,109 `mkdir()` calls
- 976 `symlink()` calls
- Result: ~4s (vs ~8s for regular copy) — 2x better, not 1000x

### The theoretical optimum

If the rootfs were packaged as a single file (disk image, tar, erofs):
- Clone time: **~10ms** (APFS clone of one file)
- Boot from it: depends on format (libkrun supports erofs via `krun_set_root_disk()`)

## Optimization Paths

### Tier 1: Use a smaller base image (Easy — ~2x improvement)

| Image | Create Time | Delta vs netshoot |
|-------|-------------|-------------------|
| Custom alpine+openssh (~30MB) | ~3.5s | -58% |
| netshoot (633MB) | ~8.4s | baseline |

Build a `devd/base` image: Alpine + openssh-server + curl + basic tools. ~30MB rootfs. Create time drops to ~3.5s (dominated by fixed overhead).

**Effort:** Low (write Dockerfile, publish to registry)  
**Impact:** ~8.4s → ~3.5s

### Tier 2: Separate pull from create (`devd pull`)

Pre-download images so `devd create` only pays the VFS copy cost:
```
devd pull nicolaka/netshoot    # background, one-time
devd create --name myapp       # only VFS copy, no network wait
```

Doesn't reduce create time, but eliminates the surprise 100s+ download on first run.

**Effort:** Low  
**Impact:** UX improvement, no speed change

### Tier 3: Template cloning with APFS (Medium — ~2x on top of Tier 1)

Maintain a "golden rootfs" per image. For subsequent VMs from the same image:
1. `cp -Rc` the template directory (APFS per-file clone)
2. Register the clone as a buildah container manually
3. Update krunvm's TOML config

Bypasses the 3.2s fixed `buildah from` overhead.

| Scenario | Create Time |
|----------|-------------|
| Current (krunvm create netshoot) | 8.4s |
| APFS clone of netshoot rootfs | ~4.0s |
| APFS clone of custom base (~30MB) | ~0.1s |

**Effort:** Medium (needs buildah storage format understanding)  
**Impact:** ~8.4s → ~4.0s (netshoot), or ~0.1s (small image)

### Tier 4: erofs disk image + libkrun direct (Hard — ~1000x improvement)

libkrun's `krun_set_root_disk()` accepts erofs/ext4 disk images. Strategy:
1. Pull image → extract → convert to erofs (one-time)
2. APFS-clone the erofs file per VM (~10ms)
3. Boot VM with `krun_set_root_disk()`

This achieves **~10ms create time** regardless of image size.

**Trade-offs:**
- Requires bypassing krunvm and calling libkrun via C FFI
- Contradicts the "shell out, not CGo" design decision in SPEC.md
- erofs is read-only; need a writable overlay (tmpfs or separate virtio-fs mount)

**Effort:** High  
**Impact:** ~8.4s → ~0.01s

### Tier 5: Contribute to buildah upstream (Long-term)

- Add APFS-aware VFS driver using `clonefile()` instead of `cp`
- Or: implement a macOS storage driver that uses disk images + APFS clones
- Would fix the root cause for all buildah/krunvm users on macOS

**Effort:** Very high (upstream contribution)  
**Impact:** Benefits entire ecosystem

## Recommended Strategy

| Phase | Action | Create Time | Effort |
|-------|--------|-------------|--------|
| Now | Custom small base image | ~3.5s | Low |
| Soon | `devd pull` for pre-caching | ~3.5s (no surprise) | Low |
| Next | Template cloning (APFS `cp -Rc`) | ~0.5s (small image) | Medium |
| Later | erofs + libkrun direct | ~0.01s | High |

The combination of a small base image (~30MB) + template cloning (~0.1s APFS clone) gets us to **sub-second create** without abandoning the shell-out architecture.

## How to Reproduce

```bash
# Profile krunvm create
time krunvm create --name test --cpus 2 --mem 512 docker.io/nicolaka/netshoot

# Profile buildah from directly
time buildah from --os linux --arch arm64 docker.io/nicolaka/netshoot

# Test APFS clone speed
ROOTFS=$(du -s ~/.local/share/containers/storage/vfs/dir/*/ | sort -rn | head -1 | awk '{print $2}')
time cp -Rc "$ROOTFS" /tmp/rootfs-clone
dd if=/dev/zero of=/tmp/bigfile bs=1m count=633
time cp -c /tmp/bigfile /tmp/bigfile-clone
```
