# Experiment 5: Proxy-Based Routing and Multi-VM Switch

**Date:** 2026-02-15  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64  
**Image:** nicolaka/netshoot  
**Depends on:** Experiment 4 (host `0.0.0.0` pre-emption)

## Hypothesis

Building on Experiment 4's finding that host `0.0.0.0` pre-emption blocks TSI while preserving guest loopback, we can build a complete proxy-based switch architecture:

1. Host proxy daemon binds `0.0.0.0:<contested-port>` (blocks TSI)
2. Each VM runs a socat relay on a unique non-contested port (TSI exposes these)
3. Proxy forwards incoming connections to the active VM's relay port
4. Switching = changing which relay port the proxy targets

## Scripts

| File | Purpose |
|------|---------|
| `exp5-proxy.py` | TCP proxy daemon. Binds `0.0.0.0:<port>`, reads target from a file on each connection. Switching = write new target to file. |
| `exp5-guest.sh` | Guest script. Starts HTTP server on `:8080` + socat relay on `:<relay_port>` → `localhost:8080`. Args: `<label> <relay_port> [alive_seconds]`. |
| `exp5-host-tests.sh` | Host-side test runner. Phases `5a` (single VM) and `5b` (two-VM switch with latency measurement). |

## Method

### Exp5a: Single VM — proxy routing

1. Write `target.txt` with `127.0.0.1:9001`
2. Start proxy: `python3 exp5-proxy.py 8080 target.txt`
3. Verify proxy holds `*:8080` via `lsof`
4. Create VM: `krunvm create --name exp5a -v /path/to/devd:/devd nicolaka/netshoot`
5. Start VM: `krunvm start exp5a /bin/bash /devd/experiments/exp5-guest.sh vm-a 9001 300`
6. Observe guest self-tests (loopback + relay)
7. Run host tests: `bash exp5-host-tests.sh 5a`

### Exp5b: Two VMs — proxy switch

1. Keep proxy and VM-A from Exp5a running
2. Create VM-B: relay on `:9002`
3. Start VM-B: `krunvm start exp5b /bin/bash /devd/experiments/exp5-guest.sh vm-b 9002 300`
4. Run host tests: `bash exp5-host-tests.sh 5b` (includes switch and latency measurement)
5. Manual reverse switch test: A→B→A→B

## Results

### Exp5a: Single VM Proxy Routing

**Guest side (VM-A):**

| Check | Result |
|-------|--------|
| `strace bind(0.0.0.0:8080)` | `= 0` (success) |
| `ss -tlnp` | Shows listener on `0.0.0.0:8080` (kernel fallback, as expected) |
| `curl 127.0.0.1:8080` (direct) | `"from-vm-a"` — own server |
| `curl 127.0.0.1:9001` (via relay) | `"from-vm-a"` — relay works |

**Host side:**

| Check | Result |
|-------|--------|
| `lsof -i :8080` | Proxy (Python) `*:8080 LISTEN` — only listener |
| `lsof -i :9001` | krunvm `*:9001 LISTEN` — TSI exposed relay |
| `curl localhost:8080` | **`"from-vm-a"`** — full proxy chain works |
| 5 consecutive curls | 5/5 `"from-vm-a"` |
| `curl localhost:9001` (direct relay) | `"from-vm-a"` |

**Proxy chain:** `client → proxy:8080 → localhost:9001 (TSI) → VM socat relay → VM localhost:8080 → VM server`

### Exp5b: Two VMs with Switching

**Guest side (VM-B):**

| Check | Result |
|-------|--------|
| `strace bind(0.0.0.0:8080)` | `= 0` (success) |
| `ss -tlnp` | Shows listener on `0.0.0.0:8080` (kernel fallback) |
| `curl 127.0.0.1:8080` (direct) | `"from-vm-b"` — own server |
| `curl 127.0.0.1:9002` (via relay) | `"from-vm-b"` — relay works |

**Host side — before switch (target = VM-A):**

| Check | Result |
|-------|--------|
| `lsof` listeners | Proxy `*:8080`, VM-A `*:9001`, VM-B `*:9002` |
| `curl localhost:8080` (×5) | 5/5 `"from-vm-a"` |
| `curl localhost:9001` (direct) | `"from-vm-a"` |
| `curl localhost:9002` (direct) | `"from-vm-b"` |

**Switch: write `127.0.0.1:9002` to target.txt**

| Check | Result |
|-------|--------|
| First curl after switch | **`"from-vm-b"` — 19.8ms latency** |
| 5 consecutive curls | 5/5 `"from-vm-b"` |

**Reverse switch: target back to VM-A**

| Check | Result |
|-------|--------|
| 5 curls after switch to VM-A | 5/5 `"from-vm-a"` |
| 5 curls after switch back to VM-B | 5/5 `"from-vm-b"` |

## Key Findings

### 1. Full proxy chain works end-to-end

`host:8080 → proxy → relay:9001 (TSI) → VM socat → VM localhost:8080 → VM server`

The proxy daemon on the host accepts connections on the contested port and forwards them to the active VM via a non-contested relay port that TSI exposes. No SSH tunnels needed for the basic case — socat relays inside the VMs are sufficient.

### 2. Guest loopback works for ALL VMs simultaneously

Both VM-A and VM-B have working `curl localhost:8080` returning their own content. This is because the host proxy's `0.0.0.0:8080` binding blocks TSI, causing both VMs to fall back to real kernel sockets. Each VM's loopback is truly local and independent.

### 3. Switching is instant (~20ms)

Changing the proxy target (writing a new relay port to the file) takes effect on the next connection. Measured latency: **19.8ms** including the curl round-trip. There is no gap, no server restart, no port release/reclaim.

### 4. Switching is fully reversible and repeatable

A→B→A→B switching works cleanly. No state corruption, no stale connections. Each switch immediately routes all new connections to the new target.

### 5. Both VMs are independently reachable via relay ports

Even while the proxy routes `host:8080` to one VM, the other VM is still reachable directly via its relay port (`:9001` or `:9002`). This means background tasks, health checks, etc. can reach any VM at any time.

## Architecture Validated

```
                    ┌─────────────┐
  browser/curl ───► │ devd proxy  │
                    │ 0.0.0.0:8080│
                    └──────┬──────┘
                           │ reads target.txt
                    ┌──────┴──────┐
                    ▼             ▼
              relay :9001   relay :9002
              (TSI exposes) (TSI exposes)
                    │             │
              ┌─────┴─────┐ ┌────┴──────┐
              │   VM-A    │ │   VM-B    │
              │  socat    │ │  socat    │
              │ 9001→8080 │ │ 9002→8080 │
              │           │ │           │
              │ server    │ │ server    │
              │ :8080     │ │ :8080     │
              │ (local)   │ │ (local)   │
              └───────────┘ └───────────┘
              loopback ✓     loopback ✓
```

`devd switch frontend` = write `127.0.0.1:9002` to target.txt. Done.
