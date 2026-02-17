/*
 * devd-vm: thin libkrun wrapper for booting microVMs.
 *
 * Replaces krunvm for devd's VM lifecycle. Calls libkrun's C API directly,
 * avoiding buildah/VFS entirely. devd shells out to this binary the same way
 * it currently shells out to krunvm.
 *
 * Two rootfs modes:
 *   --root <dir>    Directory served as root via virtio-fs (like krunvm).
 *                    Use with APFS-cloned template directories for fast create.
 *   --disk <path>   Raw ext4 disk image as root block device.
 *                    Use with APFS-cloned images for ~10ms create.
 *
 * Usage:
 *   devd-vm --root /path/to/rootfs --cpus 2 --mem 512 \
 *           --virtiofs devd:/home/user/.devd \
 *           --virtiofs ws:/home/user/project \
 *           -- /bin/sh /devd/workspaces/myapp/init.sh
 *
 * The process blocks until the VM exits. devd manages it via PID + signals,
 * identical to how it manages krunvm processes today.
 *
 * Requires: libkrun (brew install krunvm pulls it in)
 * Build:    make devd-vm  (see Makefile)
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <stdint.h>
#include <libkrun.h>

#define MAX_VIRTIOFS 8
#define MAX_ENV 64
#define PROG "devd-vm"

struct virtiofs_mount {
	const char *tag;
	const char *path;
};

struct vm_config {
	const char *root_dir;
	const char *disk_path;
	uint8_t cpus;
	uint32_t mem_mib;
	const char *workdir;
	uint32_t log_level;

	struct virtiofs_mount fs[MAX_VIRTIOFS];
	int fs_count;

	const char *env[MAX_ENV + 1];
	int env_count;

	const char *exec_path;
	const char **exec_argv;
};

static void usage(void)
{
	fprintf(stderr,
		"Usage: " PROG " [options] -- <exec-path> [args...]\n"
		"\n"
		"Options:\n"
		"  --root <dir>          Root directory (virtio-fs)\n"
		"  --disk <path>         Root disk image (raw ext4)\n"
		"  --cpus <n>            vCPUs (default: 2)\n"
		"  --mem <n>             RAM in MiB (default: 512)\n"
		"  --virtiofs <tag:path> Add virtio-fs mount (max %d, repeatable)\n"
		"  --env <KEY=VAL>      Environment variable (max %d, repeatable)\n"
		"  --workdir <path>      Working directory inside guest\n"
		"  --log-level <0-5>     libkrun log level (default: 0 = off)\n"
		"\n"
		"Exactly one of --root or --disk is required.\n"
		"Everything after -- is the exec command.\n"
		"\n"
		"A minimal default environment (HOME, PATH, TERM) is always set.\n"
		"Use --env to add more variables.\n",
		MAX_VIRTIOFS, MAX_ENV);
}

static bool parse_virtiofs(const char *arg, struct virtiofs_mount *m)
{
	const char *colon = strchr(arg, ':');
	if (!colon || colon == arg || colon[1] == '\0') {
		fprintf(stderr, PROG ": --virtiofs must be tag:path, got: %s\n", arg);
		return false;
	}

	size_t tag_len = (size_t)(colon - arg);
	char *tag = malloc(tag_len + 1);
	if (!tag) {
		fprintf(stderr, PROG ": out of memory\n");
		return false;
	}
	memcpy(tag, arg, tag_len);
	tag[tag_len] = '\0';

	m->tag = tag;
	m->path = colon + 1;
	return true;
}

static bool parse_args(int argc, char **argv, struct vm_config *cfg)
{
	memset(cfg, 0, sizeof(*cfg));
	cfg->cpus = 2;
	cfg->mem_mib = 512;
	cfg->workdir = "/";

	int i = 1;
	while (i < argc) {
		if (strcmp(argv[i], "--") == 0) {
			i++;
			break;
		}
		if (strcmp(argv[i], "--root") == 0 && i + 1 < argc) {
			cfg->root_dir = argv[++i];
		} else if (strcmp(argv[i], "--disk") == 0 && i + 1 < argc) {
			cfg->disk_path = argv[++i];
		} else if (strcmp(argv[i], "--cpus") == 0 && i + 1 < argc) {
			cfg->cpus = (uint8_t)atoi(argv[++i]);
		} else if (strcmp(argv[i], "--mem") == 0 && i + 1 < argc) {
			cfg->mem_mib = (uint32_t)atoi(argv[++i]);
		} else if (strcmp(argv[i], "--virtiofs") == 0 && i + 1 < argc) {
			if (cfg->fs_count >= MAX_VIRTIOFS) {
				fprintf(stderr, PROG ": too many --virtiofs (max %d)\n", MAX_VIRTIOFS);
				return false;
			}
			if (!parse_virtiofs(argv[++i], &cfg->fs[cfg->fs_count]))
				return false;
			cfg->fs_count++;
		} else if (strcmp(argv[i], "--env") == 0 && i + 1 < argc) {
			if (cfg->env_count >= MAX_ENV) {
				fprintf(stderr, PROG ": too many --env (max %d)\n", MAX_ENV);
				return false;
			}
			cfg->env[cfg->env_count++] = argv[++i];
		} else if (strcmp(argv[i], "--workdir") == 0 && i + 1 < argc) {
			cfg->workdir = argv[++i];
		} else if (strcmp(argv[i], "--log-level") == 0 && i + 1 < argc) {
			cfg->log_level = (uint32_t)atoi(argv[++i]);
		} else if (strcmp(argv[i], "-h") == 0 || strcmp(argv[i], "--help") == 0) {
			usage();
			exit(0);
		} else {
			fprintf(stderr, PROG ": unknown option: %s\n", argv[i]);
			return false;
		}
		i++;
	}

	if (!cfg->root_dir && !cfg->disk_path) {
		fprintf(stderr, PROG ": one of --root or --disk is required\n");
		return false;
	}
	if (cfg->root_dir && cfg->disk_path) {
		fprintf(stderr, PROG ": --root and --disk are mutually exclusive\n");
		return false;
	}
	if (i >= argc) {
		fprintf(stderr, PROG ": no exec command after --\n");
		return false;
	}

	cfg->exec_path = argv[i];
	cfg->exec_argv = (const char **)&argv[i + 1];

	return true;
}

#define KRUN_CHECK(call, msg) do { \
	int32_t _rc = (call); \
	if (_rc < 0) { \
		fprintf(stderr, PROG ": %s failed (%d)\n", (msg), _rc); \
		return 1; \
	} \
} while (0)

int main(int argc, char **argv)
{
	struct vm_config cfg;
	if (!parse_args(argc, argv, &cfg)) {
		usage();
		return 1;
	}

	if (cfg.log_level > 0)
		krun_set_log_level(cfg.log_level);

	int32_t ctx = krun_create_ctx();
	if (ctx < 0) {
		fprintf(stderr, PROG ": krun_create_ctx failed (%d)\n", ctx);
		return 1;
	}
	uint32_t ctx_id = (uint32_t)ctx;

	KRUN_CHECK(krun_set_vm_config(ctx_id, cfg.cpus, cfg.mem_mib),
		   "krun_set_vm_config");

	if (cfg.root_dir) {
		KRUN_CHECK(krun_set_root(ctx_id, cfg.root_dir),
			   "krun_set_root");
	} else {
		KRUN_CHECK(krun_set_root_disk(ctx_id, cfg.disk_path),
			   "krun_set_root_disk");
	}

	for (int i = 0; i < cfg.fs_count; i++) {
		KRUN_CHECK(krun_add_virtiofs(ctx_id, cfg.fs[i].tag, cfg.fs[i].path),
			   "krun_add_virtiofs");
	}

	KRUN_CHECK(krun_set_workdir(ctx_id, cfg.workdir),
		   "krun_set_workdir");

	/*
	 * Build a small explicit envp. Passing NULL to krun_set_exec inherits
	 * the entire host environment, which can overflow the FDT on hosts
	 * with large environments (Nix, devenv, Homebrew paths, etc.).
	 */
	const char *envp[MAX_ENV + 4];
	int ei = 0;
	envp[ei++] = "HOME=/root";
	envp[ei++] = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
	envp[ei++] = "TERM=xterm-256color";
	for (int i = 0; i < cfg.env_count && ei < MAX_ENV + 3; i++)
		envp[ei++] = cfg.env[i];
	envp[ei] = NULL;

	KRUN_CHECK(krun_set_exec(ctx_id, cfg.exec_path, cfg.exec_argv, envp),
		   "krun_set_exec");

	int32_t rc = krun_start_enter(ctx_id);
	if (rc < 0) {
		fprintf(stderr, PROG ": krun_start_enter failed (%d)\n", rc);
		return 1;
	}

	return 0;
}
