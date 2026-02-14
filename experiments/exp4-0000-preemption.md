# Experiment 4: Host `0.0.0.0` Pre-emption of TSI Port Binding

**Date:** 2026-02-15  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64  
**Image:** nicolaka/netshoot

## Hypothesis

If the host binds `0.0.0.0:<port>` (without `SO_REUSEPORT`) **before** a VM starts, TSI's host-side bind on the same address should fail (`EADDRINUSE`). This would give the host daemon exclusive ownership of the port while still allowing the guest's own `bind()` to succeed internally.

The key question: does guest loopback still work when TSI's host-side bind is blocked?

## Scripts

| File | Purpose |
|------|---------|
| `exp4-host-listener.py` | Host-side listener on `0.0.0.0:8080`. Serves `"from-host"`. Supports `--reuseport` flag. |
| `exp4-guest.sh` | Guest-side server on `0.0.0.0:8080`. Serves `"from-guest"`. Runs `strace` on bind/listen, `ss -tlnp`, and self-tests via curl. |
| `exp4-host-tests.sh` | Host-side test runner. Checks `lsof`, runs 10 consecutive curls, reports who responds. |
| `exp4-control-guest.sh` | Control test: same as guest script but run WITHOUT host pre-emption, to compare `ss -tlnp` behavior. |

## Method

### Experiment 4a: Host pre-empts with `0.0.0.0:8080` (no `SO_REUSEPORT`)

1. Start host listener: `python3 exp4-host-listener.py 8080`
2. Confirm with `lsof -i :8080` — one listener on `*:8080`
3. Create VM: `krunvm create --name exp4a -v /path/to/devd:/devd nicolaka/netshoot`
4. Start VM: `krunvm start exp4a /bin/bash /devd/experiments/exp4-guest.sh`
5. Observe guest self-test output
6. Run host tests: `bash exp4-host-tests.sh 8080`

### Control: VM without host pre-emption

1. No host listener running
2. Create and start VM with `exp4-control-guest.sh`
3. Compare `ss -tlnp` output and `lsof` on host

## Results

### Experiment 4a: Pre-emption (no `SO_REUSEPORT`)

**Guest side:**

| Check | Result |
|-------|--------|
| `strace bind(0.0.0.0:8080)` | `= 0` (success) |
| `strace listen(3, 5)` | `= 0` (success) |
| `ss -tlnp` | **Shows listener** `0.0.0.0:8080` |
| `curl 127.0.0.1:8080/index.html` | `"from-guest"` — **own server** |
| `curl localhost:8080/index.html` | `"from-guest"` — **own server** |

**Host side:**

| Check | Result |
|-------|--------|
| `lsof -i :8080` listeners | **1** (host Python only, PID 25741) |
| TSI/krunvm `*:8080 LISTEN` | **Not present** — TSI bind failed |
| `curl localhost:8080` (×10) | `"from-host"` 10/10 — host daemon wins |
| `curl 0.0.0.0:8080` | `"from-host"` |

### Control: No pre-emption

**Guest side:**

| Check | Result |
|-------|--------|
| `strace bind(0.0.0.0:8080)` | `= 0` (success) |
| `ss -tlnp` | **Empty** (just header, no listeners) |
| `curl 127.0.0.1:8080/index.html` | `"from-control"` — own server |

**Host side:**

| Check | Result |
|-------|--------|
| `lsof -i :8080` | `krunvm *:http-alt (LISTEN)` — TSI exposed |
| `curl localhost:8080` | `"from-control"` — guest serves |

## Key Findings

### 1. Host `0.0.0.0` pre-emption blocks TSI's host-side bind

When the host binds `0.0.0.0:<port>` (without `SO_REUSEPORT`) before the VM starts, TSI's attempt to `bind(0.0.0.0:<port>)` on the host side fails. Only the host daemon's listener appears in `lsof`. The host daemon has **exclusive ownership** of the port.

### 2. Guest `bind()` still succeeds despite TSI host-side failure

The guest's `bind(0.0.0.0:8080)` call returns `0` (success) as reported by `strace`. TSI does not propagate the host-side bind failure to the guest. The guest process believes it is listening normally.

### 3. Guest loopback is truly local when TSI is pre-empted

This is the critical finding: `curl localhost:8080` inside the guest returns `"from-guest"`, not `"from-host"`. This means the guest's socket operations are **not** being proxied through TSI for this port. The guest kernel handles the connection locally.

Evidence: `ss -tlnp` shows a real listener in the guest's kernel socket table. In the control test (no pre-emption), `ss -tlnp` is empty because TSI intercepts below the kernel socket table. The presence of a real listener under pre-emption confirms the guest fell back to normal kernel socket handling.

### 4. The proxy architecture is viable

This combination — host owns the port, guest has true loopback — means a host-side proxy daemon can:
- Accept all incoming connections on the contested port
- Route them to the correct VM (via SSH tunnel or other channel on a non-contested port)
- Without breaking guest-internal loopback (developer's `curl localhost:8080` still works inside the VM)

### 5. `SO_REUSEPORT` breaks the pre-emption (partial result)

When the host listener uses `SO_REUSEPORT`, TSI's bind succeeds (two `*:8080 LISTEN` entries in `lsof`). Guest loopback breaks — `curl localhost:8080` inside the guest returns `"from-host"` instead of `"from-guest"`. The kernel distributes connections between the two listeners unpredictably. **Do not use `SO_REUSEPORT` on the host daemon.**

## Implications for devd

This opens a **proxy-based switch architecture** as an alternative to the agent-based kill/restart approach:

1. `devd` daemon binds `0.0.0.0:<contested-ports>` on the host (no `SO_REUSEPORT`)
2. VMs start → TSI host-side bind fails → guests fall back to real kernel sockets
3. Guest loopback works normally for all VMs simultaneously
4. Daemon proxies incoming host connections to the active VM via SSH tunnel (or other channel on a non-contested port like 22)
5. `devd switch` = daemon changes which VM the tunnel points to

**Advantages over agent-based kill/restart:**
- No server processes are killed during switch
- All VMs have working loopback simultaneously
- Switching is a daemon-side operation (change tunnel target), not a guest-side operation

**Still needs verification (Experiment 5):**
- Can the daemon reach the VM's server via SSH tunnel on a non-contested port?
- Does this work with two VMs simultaneously?
- Switching speed (tear down tunnel A, establish tunnel B)
