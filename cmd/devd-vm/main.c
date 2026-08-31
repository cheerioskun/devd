/*
 * devd-vm: thin libkrun wrapper for booting microVMs.
 *
 * Runtime companion for devd. The Go CLI remains CGO-free and shells out to
 * this separately linked process for libkrun VM lifecycle operations.
 *
 * User workspaces always use --disk with one writable raw ext4 root. The
 * --helper-root/--data-disk combination is intentionally limited to devd's
 * one-time OCI-to-ext4 conversion helper; it is never a workspace backend.
 *
 * Usage:
 *   devd-vm --disk /path/to/rootfs.ext4 --cpus 2 --mem 512 \
 *           --virtiofs devd:/home/user/.devd \
 *           --virtiofs ws:/home/user/project \
 *           -- /bin/sh /devd/workspaces/myapp/init.sh
 *
 * The process blocks until the VM exits. devd manages it via PID + signals.
 *
 * Requires: libkrun and libkrunfw
 * Build:    scripts/build-runtime
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
	const char *data_disk_path;
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
		"  --disk <path>         Workspace root disk (raw ext4)\n"
		"  --helper-root <dir>   Internal image-conversion helper root\n"
		"  --data-disk <path>    Internal image-conversion target disk\n"
		"  --cpus <n>            vCPUs (default: 2)\n"
		"  --mem <n>             RAM in MiB (default: 512)\n"
		"  --virtiofs <tag:path> Add virtio-fs mount (max %d, repeatable)\n"
		"  --env <KEY=VAL>      Environment variable (max %d, repeatable)\n"
		"  --workdir <path>      Working directory inside guest\n"
		"  --log-level <0-5>     libkrun log level (default: 0 = off)\n"
		"\n"
		"Exactly one of --helper-root or --disk is required.\n"
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
		if (strcmp(argv[i], "--helper-root") == 0 && i + 1 < argc) {
			cfg->root_dir = argv[++i];
		} else if (strcmp(argv[i], "--disk") == 0 && i + 1 < argc) {
			cfg->disk_path = argv[++i];
		} else if (strcmp(argv[i], "--data-disk") == 0 && i + 1 < argc) {
			cfg->data_disk_path = argv[++i];
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
		fprintf(stderr, PROG ": one of --helper-root or --disk is required\n");
		return false;
	}
	if (cfg->root_dir && cfg->disk_path) {
		fprintf(stderr, PROG ": --helper-root and --disk are mutually exclusive\n");
		return false;
	}
	if ((cfg->data_disk_path != NULL) != (cfg->root_dir != NULL)) {
		fprintf(stderr, PROG ": --helper-root and --data-disk must be used together\n");
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

static bool env_has_key(const struct vm_config *cfg, const char *key)
{
	size_t key_len = strlen(key);
	for (int i = 0; i < cfg->env_count; i++) {
		if (strncmp(cfg->env[i], key, key_len) == 0 &&
		    cfg->env[i][key_len] == '=')
			return true;
	}
	return false;
}

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
		KRUN_CHECK(krun_add_disk3(ctx_id, "root", cfg.disk_path,
				      KRUN_DISK_FORMAT_RAW, false, false,
				      KRUN_SYNC_RELAXED),
			   "krun_add_disk3(root)");
		KRUN_CHECK(krun_set_root_disk_remount(ctx_id, "/dev/vda", "ext4", NULL),
			   "krun_set_root_disk_remount");
	}

	if (cfg.data_disk_path) {
		KRUN_CHECK(krun_add_disk3(ctx_id, "data", cfg.data_disk_path,
				      KRUN_DISK_FORMAT_RAW, false, false,
				      KRUN_SYNC_RELAXED),
			   "krun_add_disk3(data)");
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
	if (!env_has_key(&cfg, "HOME"))
		envp[ei++] = "HOME=/root";
	if (!env_has_key(&cfg, "PATH"))
		envp[ei++] = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
	if (!env_has_key(&cfg, "TERM"))
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
