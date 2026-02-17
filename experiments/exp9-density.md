# Experiment 9: VM Density & Resource Overhead

**Date:** 2026-02-17  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64 (16GB RAM, 10 CPUs)  
**Image:** nicolaka/netshoot  
**Per-VM config:** 2 vCPUs, 512MB RAM

## Question

How many microVMs can devd run simultaneously, and what is the per-VM resource overhead (RSS, CPU) at idle?

## Method

Script: `exp9-density.sh`

1. Record baseline (no VMs): free+inactive memory, zero krunvm processes
2. Incrementally create and start VMs (density-1 through density-10)
3. After each VM boots, wait 1s for settling, then measure:
   - Total krunvm RSS via `ps -eo rss,comm | grep krunvm`
   - Total krunvm CPU% via `ps -eo pcpu,comm | grep krunvm`
   - Process count
   - Boot time (start → SSH ready)
4. Compute per-VM RSS delta between consecutive measurements

Each VM runs sshd only (no user workload). This measures the floor — the minimum cost of having a workspace exist.

## Results

| VM# | Boot | Create | Total krunvm RSS | CPU% | Status |
|-----|------|--------|-----------------|------|--------|
| 1 | 0.87s | 12.99s | 129MB | 0.6% | OK |
| 2 | 0.67s | 12.44s | 260MB | 1.3% | OK |
| 3 | 0.87s | 11.81s | 396MB | 1.0% | OK |
| 4 | 0.86s | 12.95s | 528MB | 0.8% | OK |
| 5 | 1.07s | 11.21s | 658MB | 1.1% | OK |
| 6 | 0.86s | 12.80s | 787MB | 1.0% | OK |
| 7 | 0.67s | 13.02s | 916MB | 1.7% | OK |
| 8 | 0.66s | 12.36s | 1050MB | 1.2% | OK |
| 9 | 0.87s | 12.26s | 1180MB | 0.6% | OK |
| 10 | 0.87s | 12.40s | 1312MB | 0.8% | OK |

### Per-VM overhead

| Metric | Value |
|--------|-------|
| RSS per VM (delta) | ~131MB |
| CPU per VM (idle) | <0.2% |
| Boot time (mean) | 0.83s |
| Boot time (median) | 0.86s |
| Create time (mean) | 12.42s |

### Scaling projection (16GB host)

| VMs | Total RSS | % of host RAM |
|-----|-----------|---------------|
| 1 | 129MB | 0.8% |
| 5 | 658MB | 4.0% |
| 10 | 1,312MB | 8.0% |
| 20 | ~2,620MB (projected) | ~16% |
| 50 | ~6,550MB (projected) | ~40% |

## Key Findings

### 1. Per-VM overhead is ~131MB RSS at idle

Each krunvm process (one per VM) consumes ~131MB of resident memory with only sshd running. This is the VMM + guest kernel + sshd footprint. The 512MB `--memory` flag sets the guest's *available* RAM, not the host-side reservation — libkrun uses demand paging.

### 2. Growth is perfectly linear

RSS grows by ~131MB per VM with no degradation or superlinear overhead. VM #10 boots just as fast as VM #1 (0.87s vs 0.87s). No resource contention at this scale.

### 3. CPU is negligible at idle

All 10 VMs together use <1% CPU when idle. Each krunvm process sleeps until the guest generates work. There is no polling, no background overhead.

### 4. Boot time is constant regardless of density

Adding more VMs does not slow down boot time. The 0.66s–1.07s range across all 10 VMs is within normal variance, not a trend. The krunvm start path is independent per VM.

### 5. Create time is constant (~12s) and the bottleneck

`krunvm create` (OCI image extraction via buildah) takes ~12s regardless of how many VMs exist. This is the dominant cost of spinning up a new workspace. Boot itself is sub-second.

## Comparison: devd vs Docker Desktop

| Metric | Docker Desktop | devd (10 VMs) |
|--------|---------------|---------------|
| Idle background RAM | 2–4GB (LinuxKit VM) | 0GB (no VMs = no processes) |
| 10 workspaces running | 2–4GB + container overhead | 1.3GB total |
| Per-workspace isolation | Shared kernel | Separate kernel per VM |
| Idle CPU | 1–3% (VM always running) | <1% total for 10 VMs |

## How to Run

```bash
go build -o bin/devd ./cmd/devd
bash experiments/exp9-density.sh 10
```
