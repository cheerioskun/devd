# Experiment 13: ext4 Root-Disk Performance

**Status:** PASS on macOS ARM64 (2026-08-30)  
**Platform:** macOS ARM64 / APFS  
**Runtime:** libkrun through the experiment-only `devd-vm` launcher  
**Default image:** `nicolaka/netshoot`  
**Depends on:** Experiment 12

## Question

Does replacing devd's current Buildah VFS directory root with an APFS-cloned
raw ext4 image throttle storage performance inside the guest?

Experiment 12 established that the image can be cloned and booted quickly and
safely. This experiment measures the cost, if any, of adding ext4 and
virtio-blk between guest filesystem operations and the APFS backing file.

## Storage paths being compared

### Current devd-style root

```text
guest file operation
    → libkrun virtio-fs
    → Buildah VFS rootfs directory
    → APFS files
```

### Proposed ext4 root

```text
guest file operation
    → ext4
    → libkrun virtio-blk
    → raw sparse disk file
    → APFS
```

Both VMs use the same libkrun version, firmware, CPU count, memory, OCI rootfs,
init, benchmark binary, and host. The experiment uses the direct launcher only
to select the root-storage API. Its directory mode calls `krun_set_root`, the
same libkrun directory-root primitive underlying the current krunvm path.

## Important product distinction

The project checkout remains a separate host-mounted virtio-fs filesystem at
`/workspace` in the proposed architecture. An ext4 root therefore cannot
intrinsically slow project-file I/O: operations under `/workspace` still take
the existing path.

The experiment nevertheless benchmarks `/workspace` from both VMs as a
control. Similar results there show that the guest root choice does not produce
an indirect regression through VM configuration, memory pressure, or device
setup.

The root comparison matters for operations outside the project mount, such as:

- package installation and system updates;
- language and compiler caches under `/root` or `/var`;
- container tools and downloaded SDKs;
- workspace state intentionally stored on the guest disk;
- small-file metadata and durable writes.

## Hypothesis

1. ext4/virtio-blk will not reduce the median throughput of any measured guest
   root workload by more than 20% relative to directory-root virtio-fs.
2. ext4 will not increase median `fsync` latency by more than 25%.
3. `/workspace` performance will be substantially the same in both VMs because
   both use the same host virtio-fs mount.
4. The ext4 filesystem will remain clean after the benchmark and graceful
   shutdown.

A large ext4 improvement is plausible, especially for metadata operations:
virtio-blk exposes a Linux-native block device and ext4 handles directory and
inode operations in the guest instead of translating every operation through
macOS virtio-fs. The experiment does not assume that result.

## Method

Script: [`exp13-ext4-disk-performance.sh`](./exp13-ext4-disk-performance.sh)  
Guest helper: [`exp13-disk-bench/main.go`](./exp13-disk-bench/main.go)

### 1. Build a controlled benchmark helper

Cross-compile a static, no-CGO Linux ARM64 Go binary. Keeping the workload in a
single versioned helper avoids depending on optional tools such as `fio` in the
user's OCI image.

The helper uses non-zero deterministic data and reports JSON. Each invocation
creates a fresh benchmark directory and removes it afterward.

### 2. Prepare equivalent roots

1. Create one temporary Buildah container from the selected OCI image.
2. Inject the same init and benchmark helper.
3. Use the Experiment 12 helper-VM conversion path to copy that directory tree
   into a sparse ext4 template.
4. APFS-clone the immutable template to a writable benchmark disk.
5. Keep the original Buildah root mounted for the directory-root VM.

This compares equivalent filesystem contents. The ext4 clone includes the
first-write APFS copy-on-write behavior that a product workspace will have.

### 3. Boot both VMs

Boot the directory root and ext4 root concurrently with:

- 2 vCPUs and 512 MiB RAM by default;
- separate TSI SSH ports;
- the same host directory mounted at `/workspace` with virtio-fs;
- no other active benchmark workload.

Benchmarks run serially, not concurrently, so the two storage modes do not
intentionally contend for host I/O. Their order reverses each round to reduce
systematic warm-cache and ordering bias.

### 4. Run the workload matrix

Default root rounds: 5. Default workspace control rounds: 3.

| Workload | Default | Reported metric |
|---|---:|---:|
| Sequential write plus final `fsync` | 256 MiB | MiB/s |
| Sequential read after guest cache drop | 256 MiB | MiB/s |
| Random 4 KiB read after guest cache drop | 25,000 ops | IOPS |
| Buffered random 4 KiB write plus final `fsync` | 25,000 ops | IOPS |
| 4 KiB overwrite plus `fsync` each operation | 200 ops | p50/p95 ms |
| Small-file create | 5,000 files | ops/s |
| Small-file `stat` | 5,000 files | ops/s |
| Small-file rename | 5,000 files | ops/s |
| Small-file delete | 5,000 files | ops/s |

The helper calls `sync` and writes `3` to `/proc/sys/vm/drop_caches` before the
read tests. This drops the guest page cache but cannot evict macOS's host page
cache without globally disrupting the machine. Results therefore represent
normal repeated use on a development host, not raw-device cold media latency.
The alternating order and multiple rounds mitigate, but cannot remove, host
cache effects.

### 5. Summarize and validate

Use the median across rounds. The generated table reports `ext4 / directory`:

- for throughput and IOPS, greater than 1 is faster;
- for latency, less than 1 is faster.

The experiment flags a product-blocking regression if any guest-root throughput
metric is below 0.80x or either `fsync` latency metric is above 1.25x. It also
requires every guest cache drop to succeed and runs `e2fsck -fn` after clean
shutdown.

The `/workspace` control is reported but is not folded into the root-disk
threshold. It uses the same storage backend in both VMs; deviations indicate
benchmark noise or an indirect VM-level effect rather than an ext4 data path.

## Running it

After loading the project devenv:

```bash
bash experiments/exp13-ext4-disk-performance.sh nicolaka/netshoot
```

A shorter diagnostic run is available with:

```bash
ROOT_ROUNDS=2 WORKSPACE_ROUNDS=1 SIZE_MIB=128 \
  bash experiments/exp13-ext4-disk-performance.sh nicolaka/netshoot
```

Keep raw JSON, logs, images, and the generated Markdown summary with:

```bash
KEEP=1 bash experiments/exp13-ext4-disk-performance.sh
```

Like Experiment 12, the script only modifies its temporary Buildah container
and experiment work directory. It does not modify devd's database or normal
workspace records.

## Acceptance criteria

| Check | Required result |
|---|---|
| Root throughput metrics | ext4 >= 0.80x directory median |
| Root p50 and p95 `fsync` latency | ext4 <= 1.25x directory median |
| Guest cache drops | Succeed in every recorded run |
| Shared `/workspace` control | Record and inspect |
| Final `e2fsck -fn` | Clean |

## Results

Validated on an Apple M5 running macOS 26.5.1 with libkrun 1.19.4,
libkrunfw 64, Buildah 1.28.0, and a cached
`docker.io/nicolaka/netshoot:latest`. The run used the default five root rounds,
three workspace rounds, 256 MiB sequential files, 25,000 random operations,
200 fsync operations, and 5,000 metadata files. Every guest cache drop
succeeded and the final `e2fsck -fn` was clean.

Values are medians. Ratio is ext4 / directory; higher is better for throughput
and lower is better for latency.

### Guest root filesystem

| Metric | Directory root VM | ext4 root VM | ext4 / directory |
|---|---:|---:|---:|
| Sequential write | 2,722 MiB/s | 2,693 MiB/s | 0.99x |
| Sequential read | 4,451 MiB/s | 11,407 MiB/s | 2.56x |
| 4 KiB random read | 35,721 IOPS | 39,670 IOPS | 1.11x |
| 4 KiB buffered random write + sync | 26,187 IOPS | 181,351 IOPS | 6.93x |
| 4 KiB fsync p50 | 0.068 ms | 0.068 ms | 1.01x |
| 4 KiB fsync p95 | 0.078 ms | 0.080 ms | 1.02x |
| Small-file create | 4,197 ops/s | 243,081 ops/s | 57.91x |
| Small-file stat | 21,672 ops/s | 2,085,941 ops/s | 96.25x |
| Small-file rename | 3,980 ops/s | 290,218 ops/s | 72.92x |
| Small-file delete | 7,588 ops/s | 593,228 ops/s | 78.18x |

### Shared host workspace control

| Metric | Directory root VM | ext4 root VM | ext4 / directory |
|---|---:|---:|---:|
| Sequential write | 2,994 MiB/s | 2,911 MiB/s | 0.97x |
| Sequential read | 4,434 MiB/s | 4,484 MiB/s | 1.01x |
| 4 KiB random read | 34,141 IOPS | 34,854 IOPS | 1.02x |
| 4 KiB buffered random write + sync | 25,137 IOPS | 25,695 IOPS | 1.02x |
| 4 KiB fsync p50 | 0.067 ms | 0.064 ms | 0.95x |
| 4 KiB fsync p95 | 0.077 ms | 0.074 ms | 0.95x |
| Small-file create | 4,329 ops/s | 4,343 ops/s | 1.00x |
| Small-file stat | 21,260 ops/s | 21,121 ops/s | 0.99x |
| Small-file rename | 4,073 ops/s | 4,091 ops/s | 1.00x |
| Small-file delete | 7,703 ops/s | 7,842 ops/s | 1.02x |

The sub-0.1 ms fsync values are comparative, not evidence of physical-media
persistence. `krun_add_disk` selects libkrun's relaxed block sync mode on
macOS, and host filesystem caching remains active. Experiment 12's abrupt-stop
recovery and final filesystem checks remain the relevant integrity evidence.
Production should choose and document its block sync mode explicitly.

## Interpretation

The raw ext4 image does not throttle the guest:

- sequential writes are effectively tied at 0.99x;
- ext4 reads are 2.56x faster and random reads are 1.11x faster;
- buffered random writes are 6.93x faster;
- ext4 removes the dominant macOS virtio-fs metadata round trips, improving
  small-file operations by roughly 58–96x;
- the unchanged `/workspace` control stays within 5% on every metric, with most
  results within 2%, confirming that root-disk selection does not penalize the
  host project mount.

The large metadata result is expected rather than a claim that the physical SSD
performs millions of cold operations per second: the inode and directory work
is handled and cached by ext4 inside the guest. Directory-root virtio-fs must
cross the VM boundary and translate each operation to a macOS host file.

## Conclusion

**PASS.** No metric crossed the 20% throughput or 25% latency regression gate.
The ext4 path matches current sequential-write performance, materially improves
root reads and writes, and transforms metadata-heavy root workloads while
leaving the host-mounted project path unchanged.

Storage performance is therefore a positive reason—not merely a non-blocker—to
productize the ext4 workspace design. Product implementation should retain the
separate virtio-fs `/workspace` mount and explicitly settle relaxed versus full
block-sync policy before rollout.
