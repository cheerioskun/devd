# Experiment 15: explicit per-VM kernels and forked roots

**Date:** 2026-09-03  
**Host:** Apple Silicon, macOS 26.5.1  
**Runtime:** libkrun 1.19.4, libkrunfw 5.5.0 payload (Homebrew `libkrunfw 64`)  
**Guest:** `nicolaka/netshoot`, ext4 workspace roots

## Question

Can devd boot independently compiled kernels on separate VMs while preserving its
existing block-root, init, TSI networking, SSH, fork, and shutdown behavior?
Specifically, can a stopped workspace be cloned and then run concurrently with
its child when the two VMs use different kernels over inherited but isolated
userspace state?

## Hypothesis

Kernel selection is orthogonal to the ext4 workspace disk. A kernel built with
libkrunfw's arm64 patches and baseline configuration should accept libkrun's
injected `/init.krun`, pivot to devd's ext4 root, and reach TSI-backed SSH when
passed through `krun_set_kernel()`. A cloned root should retain the source's
pre-fork files while later writes remain isolated, regardless of which kernel
boots each disk.

## Independent kernels

Linux 6.12.91 was built from source in an isolated arm64 Linux VM. The
libkrunfw 5.5.0 patches and `config-libkrunfw_aarch64` baseline were applied
first. The two builds used different release strings and timer frequencies:

| Image | Local version | Timer frequency | SHA-256 |
|---|---|---:|---|
| kernel A | `-devd-a` | `CONFIG_HZ=100` | `1d2f81a25616acb6c0855c08e17382886a82b9b24897cfe4ccd3ee143eefe98b` |
| kernel B | `-devd-b` | `CONFIG_HZ=1000` | `4cf8544bb377d24fac2e1b480c90b87aa6f0643d0de4496b1b5ea4fb2e92d549` |

They were separate kernel compilations, not copies of the embedded libkrunfw
payload with renamed files. Both outputs were 23 MiB raw arm64 `Image` files,
and their hashes differ.

The essential build sequence inside the Linux builder was:

```bash
cp config-libkrunfw_aarch64 linux/.config
cd linux

scripts/config --set-str LOCALVERSION -devd-a
scripts/config --disable LOCALVERSION_AUTO
scripts/config --enable HZ_100
scripts/config --set-val HZ 100
make olddefconfig
make -j8 Image
cp arch/arm64/boot/Image /host/linux-6.12.91-devd-a.Image

scripts/config --set-str LOCALVERSION -devd-b
scripts/config --disable HZ_100
scripts/config --enable HZ_1000
scripts/config --set-val HZ 1000
make olddefconfig
make -j8 Image
cp arch/arm64/boot/Image /host/linux-6.12.91-devd-b.Image
```

The Alpine builder needed GNU `tar`; BusyBox `tar` does not support the
`--owner=0` option used to generate `kernel/kheaders_data.tar.xz`.

## Method

[`exp15-custom-kernel-boot.sh`](./exp15-custom-kernel-boot.sh) builds the small
[`exp15-custom-kernel-boot.c`](./exp15-custom-kernel-boot.c) launcher. The
launcher reproduces `devd-vm`'s raw ext4 disk, block-root remount, `devd`
virtio-fs mount, environment, and `/usr/local/sbin/devd-init` execution. It
performs no compatibility inspection of an external kernel.

The script then:

1. Extracts libkrunfw's embedded kernel as a control.
2. Uses `devd run` to create a real source workspace and writes
   `/root/exp15-inherited` before stopping it.
3. Direct-boots the source disk with the implicit kernel, the extracted kernel,
   and the extracted kernel plus an explicit empty initramfs and custom command
   line.
4. Uses the product `devd fork` path to clone the stopped source disk, confirms
   that the child inherited the marker, writes a child-only marker, and stops
   the child.
5. Concurrently boots the source with kernel A and the child with kernel B.
6. Confirms different `uname -r` values, inherited state in both guests, and
   isolation of writes made after the clone.
7. Requests guest shutdown and confirms that both launcher processes exit.

Run it after building the product binaries and supplying the two raw kernels:

```bash
build
experiments/exp15-custom-kernel-boot.sh nicolaka/netshoot \
  ~/repos/linux-6.12.91-devd-a.Image \
  ~/repos/linux-6.12.91-devd-b.Image
```

`KERNEL_A` and `KERNEL_B` environment variables may be used instead of the
second and third arguments. Set `KEEP=1` to retain logs and extracted artifacts
under `/tmp/devd-exp15` (or the platform's `$TMPDIR`).

## Results

All control launches reached SSH. The implicit and extracted-kernel cases both
reported `6.12.91` and had identical `/proc/cmdline` values. The custom command
line retained `init=/init.krun` and exposed `exp15.marker=present`; the empty
initramfs did not interfere with the block-root pivot.

The source and child then ran simultaneously with genuinely different kernels:

```text
implicit                 kernel=6.12.91
explicit-embedded        kernel=6.12.91
explicit-initramfs       kernel=6.12.91
kernel-a-source          kernel=6.12.91-devd-a
kernel-b-child           kernel=6.12.91-devd-b
Experiment 15: PASS
Implicit kernel boot: PASS
Extracted embedded kernel: PASS
Explicit initramfs and cmdline: PASS
Independent kernel A: 6.12.91-devd-a
Independent kernel B: 6.12.91-devd-b
Fork inherited userspace state: PASS
Fork write isolation: PASS
Concurrent operation: PASS
Clean shutdown: PASS
```

Both guests read the source's pre-fork marker. The child retained its child-only
file, the source could not see that file, and the child could not see a new
source-only file. Both VM processes remained alive while these cross-checks ran.
Both exited after the normal guest shutdown request.

## Conclusion

The experiment supports persistent per-workspace kernel selection. devd can
boot different, independently compiled kernels concurrently over a parent and
forked child without coupling kernel identity to the root disk or sacrificing
userspace inheritance and write isolation.

A production implementation should:

- store kernel selection in the workspace spec so it survives `stop`/`start`;
- inherit that selection on `fork`, while allowing an explicit child override;
- pass absolute host paths through `devd-vm` to `krun_set_kernel()`;
- preserve NULL for omitted initramfs and command line;
- default arm64 kernels to `raw` and amd64 kernels to `elf`, matching libkrun's
  upstream external-kernel example;
- avoid compatibility policy beyond basic path and regular-file checks;
- leave failed boots observable in the VM log and stoppable by the user.

This establishes compatibility for kernels built with the libkrunfw patch set
and configuration. It does not imply that arbitrary distribution kernels have
TSI or every driver devd requires; those kernels should be attempted and
allowed to fail naturally rather than rejected by speculative validation.
