/*
 * Experiment 15 launcher: compare libkrunfw's implicit kernel with the same
 * kernel supplied explicitly through krun_set_kernel(). Not production code.
 */
#include <errno.h>
#include <inttypes.h>
#include <libkrun.h>
#include <stdint.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#if defined(__x86_64__)
#define EXP15_KERNEL_FORMAT KRUN_KERNEL_FORMAT_ELF
#else
#define EXP15_KERNEL_FORMAT KRUN_KERNEL_FORMAT_RAW
#endif

extern char *krunfw_get_kernel(uint64_t *guest_addr, uint64_t *entry_addr,
                               size_t *kernel_size);

static int check(int32_t rc, const char *operation)
{
    if (rc >= 0)
        return 0;
    fprintf(stderr, "exp15: %s: %s (%" PRId32 ")\n", operation,
            strerror(-rc), rc);
    return -1;
}

static int extract_kernel(const char *path)
{
    uint64_t guest_addr = 0;
    uint64_t entry_addr = 0;
    size_t size = 0;
    char *kernel = krunfw_get_kernel(&guest_addr, &entry_addr, &size);
    if (kernel == NULL || size == 0) {
        fprintf(stderr, "exp15: libkrunfw returned an empty kernel\n");
        return 1;
    }

    FILE *output = fopen(path, "wb");
    if (output == NULL) {
        perror("exp15: open extracted kernel");
        return 1;
    }
    if (fwrite(kernel, 1, size, output) != size || fclose(output) != 0) {
        perror("exp15: write extracted kernel");
        return 1;
    }
    fprintf(stderr,
            "exp15: extracted %zu bytes, guest=0x%" PRIx64
            ", entry=0x%" PRIx64 "\n",
            size, guest_addr, entry_addr);
    return 0;
}

static int launch(int argc, char **argv)
{
    if (argc < 5 || argc > 8) {
        fprintf(stderr,
                "usage: %s <disk> <devd-dir> <name> <ssh-port>"
                " [kernel|- [initramfs|- [cmdline|-]]]\n",
                argv[0]);
        return 2;
    }

    const char *disk = argv[1];
    const char *devd_dir = argv[2];
    const char *name = argv[3];
    const char *port = argv[4];
    const char *kernel = argc > 5 && strcmp(argv[5], "-") != 0 ? argv[5] : NULL;
    const char *initramfs = argc > 6 && strcmp(argv[6], "-") != 0 ? argv[6] : NULL;
    const char *cmdline = argc > 7 && strcmp(argv[7], "-") != 0 ? argv[7] : NULL;

    char name_env[256];
    char port_env[64];
    if (snprintf(name_env, sizeof(name_env), "DEVD_NAME=%s", name) >=
            (int)sizeof(name_env) ||
        snprintf(port_env, sizeof(port_env), "DEVD_SSH_PORT=%s", port) >=
            (int)sizeof(port_env)) {
        fprintf(stderr, "exp15: name or port is too long\n");
        return 2;
    }
    const char *envp[] = {
        "HOME=/root",
        "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "TERM=xterm-256color",
        name_env,
        port_env,
        NULL,
    };

    int32_t created = krun_create_ctx();
    if (created < 0) {
        check(created, "krun_create_ctx");
        return 1;
    }
    uint32_t ctx = (uint32_t)created;

    if (check(krun_set_vm_config(ctx, 2, 512), "krun_set_vm_config") ||
        check(krun_add_disk3(ctx, "root", disk, KRUN_DISK_FORMAT_RAW, false,
                             false, KRUN_SYNC_RELAXED),
              "krun_add_disk3") ||
        check(krun_set_root_disk_remount(ctx, "/dev/vda", "ext4", NULL),
              "krun_set_root_disk_remount") ||
        check(krun_add_virtiofs(ctx, "devd", devd_dir),
              "krun_add_virtiofs") ||
        check(krun_set_workdir(ctx, "/"), "krun_set_workdir"))
        return 1;

    if (kernel != NULL &&
        check(krun_set_kernel(ctx, kernel, EXP15_KERNEL_FORMAT, initramfs,
                              cmdline),
              "krun_set_kernel"))
        return 1;

    if (check(krun_set_exec(ctx, "/usr/local/sbin/devd-init", NULL, envp),
              "krun_set_exec"))
        return 1;

    fprintf(stderr, "exp15: booting with %s kernel\n",
            kernel == NULL ? "implicit libkrunfw" : kernel);
    int32_t rc = krun_start_enter(ctx);
    if (rc < 0) {
        check(rc, "krun_start_enter");
        return 1;
    }
    return 0;
}

int main(int argc, char **argv)
{
    if (argc == 3 && strcmp(argv[1], "--extract") == 0)
        return extract_kernel(argv[2]);
    return launch(argc, argv);
}
