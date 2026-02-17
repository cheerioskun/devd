# Experiment 8: devd Boot Benchmark & Switch Validation

**Date:** 2026-02-17  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64  
**Image:** nicolaka/netshoot  
**Depends on:** Experiments 4–7 (proxy architecture validation)

## Purpose

Two measurements:

1. **Boot benchmark** — Wall-clock time from `devd run` to first successful SSH connection, broken down into create (OCI extraction) and boot (kernel + sshd startup).
2. **Switch validation** — Confirm `devd switch` correctly routes contested-port traffic between workspaces with guest loopback isolation.

## Method

### 8a: Boot Benchmark

Script: `exp8-benchmark.sh`

1. Build devd: `go build -o bin/devd ./cmd/devd`
2. Run 5 iterations. Each iteration:
   - `devd run nicolaka/netshoot --name bench-N`
   - devd reports create time (krunvm create) and boot time (krunvm start → SSH ready) separately
   - Verify SSH key auth works
   - Cleanup: `devd rm -f bench-N`, 1s cooldown

### 8b: Switch Validation

Script: `exp8-switch-test.sh`

**Critical ordering** (per experiments 4–6): daemon pre-empts ports BEFORE VMs start.

1. `devd create --name ws-alpha --ports 8080 --cmd "http server serving 'from-alpha'"`
2. `devd create --name ws-beta --ports 8080 --cmd "http server serving 'from-beta'"`
3. `devd daemon &` — pre-empts :8080 on host, polls for tunnels
4. `devd start ws-alpha` — TSI falls back on :8080 (host already holds it)
5. `devd start ws-beta` — same
6. Verify routing, switching, and guest loopback isolation

## Results

### 8a: Boot Benchmark

| Run | Create | Boot | Total | SSH |
|-----|--------|------|-------|-----|
| 1 | 11.96s | 0.81s | 12.77s | PASS (key) |
| 2 | 10.26s | 0.40s | 10.67s | PASS (key) |
| 3 | 10.47s | 0.61s | 11.07s | PASS (key) |
| 4 | 10.18s | 0.61s | 10.79s | PASS (key) |
| 5 | 10.39s | 0.61s | 10.99s | PASS (key) |

| Stat | Create | Boot | Total |
|------|--------|------|-------|
| Min | 10.18s | 0.40s | 10.67s |
| Max | 11.96s | 0.81s | 12.77s |
| Mean | 10.65s | 0.61s | 11.26s |
| Median | 10.39s | 0.61s | 10.99s |
| Stdev | 0.74s | 0.14s | 0.86s |

### 8b: Switch Validation — 14/14 PASS

| Test | Expected | Actual | Result |
|------|----------|--------|--------|
| Initial curl 1 (beta active) | from-beta | from-beta | PASS |
| Initial curl 2 | from-beta | from-beta | PASS |
| Initial curl 3 | from-beta | from-beta | PASS |
| After switch to alpha, curl 1 | from-alpha | from-alpha | PASS |
| After switch to alpha, curl 2 | from-alpha | from-alpha | PASS |
| After switch to alpha, curl 3 | from-alpha | from-alpha | PASS |
| After switch to beta, curl 1 | from-beta | from-beta | PASS |
| After switch to beta, curl 2 | from-beta | from-beta | PASS |
| After switch to beta, curl 3 | from-beta | from-beta | PASS |
| Rapid: →alpha | from-alpha | from-alpha | PASS |
| Rapid: →beta | from-beta | from-beta | PASS |
| Rapid: →alpha(2) | from-alpha | from-alpha | PASS |
| Alpha guest loopback | from-alpha | from-alpha | PASS |
| Beta guest loopback | from-beta | from-beta | PASS |

## Analysis

### Boot time is dominated by `krunvm create` (OCI extraction)

The actual VM boot + sshd startup is **0.61s median** — sub-second as expected. But `krunvm create` (which extracts the OCI image into a rootfs via buildah) takes **~10s**, making the end-to-end time ~11s.

This is a one-time cost per workspace. Once created, `devd start` only pays the 0.6s boot cost. For a "create once, start many times" workflow, the effective boot time is sub-second.

Possible optimizations:
- Pre-pull and cache images (`devd pull`)
- Use smaller base images (Alpine ~50MB vs netshoot ~300MB)
- Explore krunvm's buildah layer for faster extraction

### Switch works correctly with pre-emption ordering

The daemon must pre-empt contested ports BEFORE VMs start. When the daemon was started after VMs (initial broken test), TSI grabbed :8080 for the first VM, and the second VM's loopback went to the wrong server — exactly what experiment 7 predicted.

With correct ordering (create → daemon → start), all 14 tests pass:
- **Routing switches instantly** — next curl after `devd switch` gets the new workspace
- **Rapid A→B→A switching** — clean, no state corruption
- **Guest loopback isolated** — both VMs reach their own servers via `localhost:8080`

### SSH key auth works

The init script sets `chmod 755 /root` (netshoot image ships /root as 775, which OpenSSH rejects for authorized_keys). All 5 benchmark runs verified SSH key auth.

## How to Run

```bash
# Build
go build -o bin/devd ./cmd/devd

# 8a: Boot benchmark (5 iterations)
bash experiments/exp8-benchmark.sh 5

# 8b: Switch validation
bash experiments/exp8-switch-test.sh
```
