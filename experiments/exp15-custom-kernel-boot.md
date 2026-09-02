# Experiment 15: explicit per-VM kernels

**Date:** 2026-09-01  
**Host:** Apple Silicon, macOS 26.5.1  
**Runtime:** libkrun 1.19.4, libkrunfw 5.5.0 payload (Homebrew `libkrunfw 64`)  
**Guest:** `nicolaka/netshoot`, ext4 workspace root

## Question

Can devd's existing block-root workspace boot unchanged when libkrun is given an
external kernel with `krun_set_kernel()`, rather than using the kernel embedded
in libkrunfw? Do libkrun's injected `/init.krun`, block-root pivot, devd init,
TSI networking, and SSH readiness still work?

## Hypothesis

On arm64, the libkrunfw kernel bundle is a raw Linux `Image` loaded at
`0x80000000`. Writing those bytes to a file and supplying that file with
`KRUN_KERNEL_FORMAT_RAW` should produce a boot equivalent to the implicit
libkrunfw path. Passing a valid empty initramfs should not prevent the later
pivot to devd's ext4 root. A NULL external-kernel command line should retain
libkrun's defaults, including `init=/init.krun`.

## Method

[`exp15-custom-kernel-boot.sh`](./exp15-custom-kernel-boot.sh) builds the small
[`exp15-custom-kernel-boot.c`](./exp15-custom-kernel-boot.c) launcher and then:

1. Calls `krunfw_get_kernel()` and writes its returned bytes to a host file.
2. Uses the real `devd run` path once to create an isolated ext4 workspace,
   inject `devd-init`, configure SSH, verify the normal product boot, and stop
   it.
3. Direct-launches that same disk with libkrun's implicit kernel.
4. Direct-launches it with the extracted kernel passed to
   `krun_set_kernel(..., KRUN_KERNEL_FORMAT_RAW, NULL, NULL)`.
5. Direct-launches it with the explicit kernel, a valid empty gzip/newc
   initramfs, and an exact custom command line containing
   `exp15.marker=present` and `init=/init.krun`.
6. For each launch, waits for SSH, records `uname -r` and `/proc/cmdline`, then
   requests a clean guest shutdown.

The launcher configures the same raw ext4 root disk, block-root remount,
`devd` virtio-fs mount, environment, and `/usr/local/sbin/devd-init` command as
`devd-vm`. It deliberately performs no kernel compatibility inspection.

Run it after building the product binaries:

```bash
build
experiments/exp15-custom-kernel-boot.sh
```

Set `KEEP=1` to retain launch logs and extracted artifacts under
`/tmp/devd-exp15`.

## Results

The extracted payload was recognized directly as a kernel image:

```text
exp15: extracted 24117248 bytes, guest=0x80000000, entry=0x80000000
kernel: Linux kernel ARM64 boot executable Image, little-endian, 4K pages
sha256: b50a4165215d5d897ab3614606a2105756cf8f2b2510cbceda9dc06057a5622d
```

All three direct launches reached SSH and shut down cleanly:

```text
implicit                 kernel=6.12.91
explicit                 kernel=6.12.91
explicit-initramfs       kernel=6.12.91
Experiment 15: PASS
Implicit kernel boot: PASS
Explicit kernel boot: PASS
Explicit initramfs: PASS
Explicit cmdline: PASS
```

The implicit and explicit-NULL-command-line cases had identical
`/proc/cmdline` values. Both contained `init=/init.krun`. The explicit custom
command line reached the guest and contained `exp15.marker=present`. The empty
initramfs was accepted, unpacked, and did not interfere with libkrun's block
root pivot.

The product preparation boot reached SSH in 0.41 seconds. The cold OCI-to-ext4
conversion took 13.71 seconds in the isolated experiment state directory; that
is unrelated to kernel selection.

## Conclusion

The kernel source is orthogonal to devd's workspace disk and userspace state.
`krun_set_kernel()` can select a kernel per VM without changing the ext4 root,
injected `/init.krun`, devd init, or lifecycle model.

The production implementation should:

- store kernel selection in the workspace spec, not the image template or DB;
- pass absolute host paths through `devd-vm` to `krun_set_kernel()`;
- preserve NULL for omitted initramfs and command line rather than passing
  empty strings;
- default arm64 kernels to `raw` and amd64 kernels to `elf`, matching libkrun's
  upstream external-kernel example;
- avoid content and compatibility validation beyond ordinary file/path checks;
- retain libkrun's default command line when the user does not specify one;
- offer a no-readiness-wait launch mode for kernels that boot but cannot provide
  devd's TSI-backed SSH path.

This experiment proves the mechanism with a known-compatible external kernel.
It intentionally does not claim that arbitrary distribution kernels provide
TSI or the drivers required by devd.
