# Experiment 11: devd-vm — Direct libkrun Wrapper

**Date:** 2026-02-18  
**Depends on:** Experiment 10 (identified create as bottleneck)

## Question

Can we bypass krunvm/buildah entirely with a thin C binary that calls libkrun directly? What speedup does this give?

## What We Built

`devd-vm`: a ~170-line C binary that links libkrun and replaces krunvm for VM lifecycle. Supports two rootfs modes:

- `--root <dir>` — directory served as guest root via virtio-fs (same as krunvm)
- `--disk <path>` — raw ext4 disk image as root block device (stub, untested)

Source archived in `experiments/devd-vm-prototype/`.

## Results

### Directory mode (`--root`) with netshoot (633MB, 15K files)

| Phase | krunvm (baseline) | devd-vm |
|-------|-------------------|---------|
| Create / Clone | 8,310ms (buildah VFS) | 6,345ms (APFS `cp -Rc`) |
| Boot → SSH ready | 600ms | 493ms |
| **Total** | **8,910ms** | **7,195ms** |

**Speedup: 1.2x** — not worth the added complexity.

### Why the directory path is a dead end

The APFS `cp -Rc` (per-file clone) is O(n-files). For any real dev environment image:

| Image size | Files | APFS clone time | Total w/ devd-vm |
|-----------|-------|-----------------|------------------|
| 30MB | ~500 | ~100ms | ~600ms |
| 200MB | ~5K | ~2s | ~2.5s |
| 633MB | ~15K | ~4-6s | ~5-7s |

The savings from eliminating buildah's 3.2s fixed overhead are eaten by the clone cost for large images. Only tiny images benefit, and those are already fast with krunvm (~3.5s).

**The middle tiers from exp10 (APFS template cloning, smaller images) are a trap for realistic dev environment images.** They help marginally but don't change the order of magnitude.

### The only path to sub-second create

Tier 4 (disk image + APFS file clone) is the only approach that scales:

| Method | 30MB image | 633MB image |
|--------|-----------|-------------|
| buildah VFS | 3.5s | 8.3s |
| APFS `cp -Rc` dir | 0.1s | 4-6s |
| **APFS `cp -c` single file** | **10ms** | **10ms** |

APFS clones a single file in O(1) regardless of size. Packaging the rootfs as an ext4 image makes create time constant at ~10ms + ~500ms boot = **~510ms total**.

## Bugs Found in libkrun Integration

### 1. FDT overflow from host environment

Passing `envp=NULL` to `krun_set_exec()` collects the entire host environment and serializes it into the Flattened Device Tree. On hosts with large environments (Nix, devenv, Homebrew — 7.5KB, 86 vars in our case), this overflows the FDT size limit.

**Symptom:** `panicked at src/vmm/src/builder.rs:594:53: called Result::unwrap() on an Err value: TooLarge`

**Fix:** Pass an explicit minimal envp (HOME, PATH, TERM) instead of NULL.

### 2. argv[0] duplication

libkrun internally prepends `exec_path` as argv[0]. If the caller also includes the program name in the argv array, the guest sees a doubled argv:

```
# Caller passes: exec_path="/bin/sh", argv={"/bin/sh", "-c", "echo hi"}
# Guest sees:    argv[0]="/bin/sh", argv[1]="/bin/sh", argv[2]="-c", argv[3]="echo hi"
# sh interprets argv[1] as a script file → tries to parse the /bin/sh binary as shell script
```

**Symptom:** `/bin/sh: line 1: syntax error: unexpected "("`

**Fix:** Start argv from the first argument after the program name.

### 3. virtiofs not auto-mounted

`krun_add_virtiofs()` creates the virtio-fs device but the guest must explicitly mount it:

```sh
mount -t virtiofs <tag> /mountpoint
```

krunvm handles this via `.krun_config.json` written into the rootfs. With devd-vm, the init script must do it, or we write the mount commands directly into the cloned rootfs.

## Conclusion

The thin C binary works and is a clean replacement for krunvm. But the performance win only materializes with the disk image path (`--disk`), which requires:

1. OCI → ext4 conversion pipeline (needs Linux tools, run inside a helper VM)
2. Guest kernel configured to boot from block device (via `krun_set_root_disk()`)
3. Writable overlay solution (ext4 is writable, but fixed-size; or overlayfs in guest)

**Recommendation:** Keep devd-vm archived. Revisit when create latency becomes a user-facing problem. For v0.1, krunvm's 8-9s create is acceptable — it's a one-time cost per workspace, not a hot path.
