# Experiment 16: Workspace ownership and host-supplied bootstrap

**Status:** PASS on macOS arm64, macOS 26.5.1, libkrun 1.19.4.

## Hypothesis

Explicit operation/disk ownership and a narrowly scoped, host-rendered guest
bootstrap can replace the old lifecycle conventions without compromising ext4
persistence, fork identity, custom kernels, or the interactive latency envelope.
In particular, updating devd's bootstrap should not require replacing a user's
persistent disk or rebuilding an OCI template.

## Method

The committed hardware driver is the expanded [`test-functional.sh`](../test-functional.sh),
not a second experiment-only implementation of the lifecycle:

```bash
check
build
SKIP_BUILD=1 bash test-functional.sh
experiments/exp15-custom-kernel-boot.sh nicolaka/netshoot \
  /path/to/linux-6.12.91-devd-a.Image \
  /path/to/linux-6.12.91-devd-b.Image
SKIP_BUILD=1 bash benchmark.sh
```

The functional suite uses isolated state/project directories and cleans up its
workspaces. It now additionally:

1. Rejects a companion CPU argument that would overflow its `uint8_t` field,
   verifying rejection happens in parsing rather than failing later at boot.
2. Verifies the guest sees its public key/current init, but not host SSH state,
   sibling workspaces, specs, root disks, or the database in `/devd`.
3. Checks the effective OpenSSH configuration disables password authentication.
4. Attempts an exclusive host flock on the running root disk; it must fail.
5. Deliberately installs an obsolete `/usr/local/sbin/devd-init` that exits 42.
   The next boot must still work using the current host-supplied bootstrap.
6. Starts the same stopped workspace from two CLI processes concurrently;
   exactly one must succeed.
7. Exercises persistence, fork inheritance, fresh identity, bidirectional project
   mounts, isolated child disk writes, shared-port switching, and cleanup.

The initial added bootstrap test failed because fresh templates no longer create
`/usr/local/sbin`. The test now explicitly creates that directory before installing
its deliberately broken script. This was a test-fixture assumption, not a failed
boot handoff.

Unit/subprocess tests use real temporary SQLite databases, real flock locks,
and fake companion/SSH executables, without CGO or VM hardware. They cover:

- cross-process operation exclusion and lock release after partial acquisition;
- pure fork inheritance, override/reset semantics, and slice independence;
- authoritative-spec migration and disposable-control rendering, including a
  hostile symlink left by a previous guest;
- exact companion argv, process receipts, exit detection, and stale PID identity;
- launch recovery when SQLite still says stopped, activation/readiness failures,
  bounded hung SSH probes, and refusing removal after a stop failure;
- refusing start/fork/removal of a disk owned by an unrecorded process;
- preserving unowned workspace files, atomic workspace/port metadata publication,
  and preserving the old active target when a new target is invalid;
- per-incarnation proxy socket ownership, rejection of stale DB process IDs,
  and rejection of work after proxy stop.

The CLI/runtime/workspace/proxy/DB tests also passed ten consecutive runs.

macOS process inspection initially exposed an important API distinction:
`unix.SysctlKinfoProc` reports EIO for a zero-length result after process exit.
`unix.SysctlKinfoProcSlice` distinguishes a genuinely empty result from inspection
errors. The runtime uses the latter, so an exited process is not confused with an
observation failure.

## Results

| Check | Result |
|---|---|
| Formatting, vet, lint, unit/subprocess tests (`check`) | PASS |
| Complete three-binary build | PASS |
| Expanded hardware functional suite | PASS |
| Distinct-kernel exp15 with corrected isolation assertions | PASS |
| Linux arm64 and amd64 CGO-free cross-builds | PASS |

Exp15 now mounts the workspace's scoped control directory and boots its rendered
init. Its direct-launch cases also hold the disk lock. Both distinct kernels
(`6.12.91-devd-a` and `6.12.91-devd-b`), fork inheritance/isolation, concurrent
operation, empty-initramfs/custom-cmdline controls, and shutdown passed. These
remain API experiments, not proof of meaningful custom early userspace.

Representative final 10-iteration product benchmark, `nicolaka/netshoot`:

| Operation | p50 | p95 | Acceptance |
|---|---:|---:|---:|
| Cached run → SSH | 334.3 ms | 439.3 ms | ≤ 1000 ms |
| Stopped start → SSH | 182.2 ms | 296.8 ms | ≤ 1000 ms |
| Fork → SSH | 334.3 ms | 446.0 ms | ≤ 1000 ms |
| Warm switch → response | 34.9 ms | 38.8 ms | ≤ 250 ms |
| Idle RSS per VM | 203.5 MiB | 203.5 MiB | ≤ 256 MiB |

Cold preparation plus first boot was 10.00 seconds, excluded from the hot-path
thresholds. These measurements establish the existing envelope still passes;
they do not establish a statistically significant speedup over prior results.

## Conclusion and limits

The new ownership boundaries preserve the product lifecycle and performance
budget on the tested host. Guest policy is now supplied at boot, separate from
persistent userspace and immutable image conversion. Only the guest-specific
control directory is exported, and key-only SSH replaces the shared password.

This is not a comprehensive security audit or a power-loss campaign. Advisory
locks protect cooperating current-version launchers, not arbitrary programs that
ignore them. Process identity checks before signals are not an atomic portable
process-handle API. Filesystem and DB changes use recoverable ordering rather than
a distributed transaction; interrupted publication/removal may leave preserved
orphan directories. Linux was cross-built, not hardware-tested in this run.
The `devenv` executable was unavailable in the session; `check`, the complete build,
and the hardware suites were invoked directly.

Before upgrade, running legacy VMs must be stopped using their original binary.
Version-1 specs upgrade on start; mixed-version lifecycle operation and downgrade
of version-2 workspaces are unsupported. TSI semantics and host project sharing
are unchanged. Inspect, meaningful initramfs boot, and explicit asynchronous
startup remain separate follow-up features.
