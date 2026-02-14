# Experiment 6: SSH Tunnel as Relay Mechanism

**Date:** 2026-02-15  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64  
**Image:** nicolaka/netshoot  
**Depends on:** Experiments 4 & 5

## Hypothesis

Experiment 5 used socat relays inside VMs to bridge the proxy to the VM's local server. This works but requires an extra process inside each VM. Since devd already runs sshd in every VM (for IDE integration), SSH port forwarding (`ssh -L`) can replace the socat relay — the tunnel is managed entirely from the host side, with no additional guest-side process.

The chain becomes: `host proxy:8080 → SSH tunnel:9001 → VM localhost:8080`

## Scripts

| File | Purpose |
|------|---------|
| `exp5-proxy.py` | Reused from Experiment 5. TCP proxy daemon on `0.0.0.0:8080`. |
| `exp6-guest.sh` | Guest script. Starts HTTP server on `:8080` + sshd on `:<ssh_port>`. No socat. Args: `<label> <ssh_port> [alive_seconds]`. |
| `exp6-host-tests.sh` | Host-side test runner. Phases `6a` (single VM) and `6b` (two-VM switch). |

## Method

### Exp6a: Single VM — SSH tunnel relay

1. Start proxy: `python3 exp5-proxy.py 8080 target.txt` (target = `127.0.0.1:9001`)
2. Create VM-A: `krunvm create --name exp6a -v .../devd:/devd nicolaka/netshoot`
3. Start VM-A: `krunvm start exp6a /bin/bash /devd/experiments/exp6-guest.sh vm-a 2222 300`
4. Verify SSH access: `sshpass -p devd ssh -p 2222 root@127.0.0.1 echo ok`
5. Create SSH tunnel: `sshpass -p devd ssh -N -L 9001:127.0.0.1:8080 root@127.0.0.1 -p 2222 &`
6. Run host tests: `bash exp6-host-tests.sh 6a`

### Exp6b: Two VMs — SSH tunnel switch

1. Keep proxy, VM-A, and tunnel from Exp6a running
2. Start VM-B with sshd on `:2223`: `exp6-guest.sh vm-b 2223 300`
3. Create second SSH tunnel: `ssh -N -L 9002:127.0.0.1:8080 root@127.0.0.1 -p 2223 &`
4. Run host tests: `bash exp6-host-tests.sh 6b` (includes switch)
5. Reverse switch test

## Results

### Exp6a: Single VM + SSH Tunnel

**Guest side (VM-A):**

| Check | Result |
|-------|--------|
| `bind(0.0.0.0:8080)` | `= 0` (success, kernel fallback) |
| `ss -tlnp` | Shows `:8080` listener (real kernel socket) |
| `curl 127.0.0.1:8080` | `"from-vm-a"` — loopback works |
| sshd on `:2222` | Running, accessible from host |
| SSH `curl localhost:8080` from inside | `"from-vm-a"` — even via SSH session, loopback is local |

**Host side:**

| Check | Result |
|-------|--------|
| `lsof` | Proxy `*:8080`, SSH tunnel `localhost:9001`, krunvm `*:2222` |
| `curl localhost:8080` (×5) | 5/5 `"from-vm-a"` via full proxy chain |
| `curl localhost:9001` (direct) | `"from-vm-a"` via SSH tunnel |

### Exp6b: Two VMs + SSH Tunnel Switch

**Guest side (VM-B):**

| Check | Result |
|-------|--------|
| `bind(0.0.0.0:8080)` | `= 0` (success) |
| `curl 127.0.0.1:8080` | `"from-vm-b"` — loopback works |
| sshd on `:2223` | Running, accessible from host |

**Host side — before switch (target = VM-A via :9001):**

| Check | Result |
|-------|--------|
| `curl localhost:8080` (×5) | 5/5 `"from-vm-a"` |
| `curl localhost:9001` (direct) | `"from-vm-a"` |
| `curl localhost:9002` (direct) | `"from-vm-b"` |

**Switch: write `127.0.0.1:9002` to target.txt:**

| Check | Result |
|-------|--------|
| First curl after switch | `"from-vm-b"` |
| 5 consecutive curls | 5/5 `"from-vm-b"` |

**Reverse switch (A→B→A→B):**

| Direction | Result |
|-----------|--------|
| → VM-A | 3/3 `"from-vm-a"` |
| → VM-B | 3/3 `"from-vm-b"` |

## Key Findings

### 1. SSH port forwarding works as the relay mechanism

The chain `proxy:8080 → SSH tunnel:9001 → VM localhost:8080` works exactly like the socat relay from Experiment 5. No guest-side relay process needed.

### 2. No additional guest-side setup required

The only guest-side requirement is sshd, which devd already runs for IDE integration. The server process runs on `:8080` as normal. All relay logic is managed from the host.

### 3. SSH tunnel is a host-side resource

Unlike the socat relay (which ran inside the VM and used TSI to expose the relay port), the SSH tunnel listener is on the host. This means:
- No TSI involvement for the relay port
- No risk of relay port contention between VMs
- devd controls tunnel lifecycle entirely from the host

### 4. Each VM needs a unique SSH port

Since TSI auto-exposes sshd ports, multiple VMs can't share the same SSH port. devd must assign unique SSH ports per VM (managed in SQLite). This is already the design.

### 5. Architecture is complete

The full `devd switch` architecture is now validated end-to-end:

```
                    ┌─────────────┐
  browser/curl ───► │ devd proxy  │
                    │ 0.0.0.0:8080│
                    └──────┬──────┘
                           │ reads target
                    ┌──────┴──────┐
                    ▼             ▼
           SSH tunnel:9001  SSH tunnel:9002
           (host-side)      (host-side)
                │                │
                │ ssh -L         │ ssh -L
                ▼                ▼
          VM-A :2222       VM-B :2223
          (sshd via TSI)   (sshd via TSI)
                │                │
                ▼                ▼
          VM-A localhost   VM-B localhost
          :8080 (local)    :8080 (local)
          loopback ✓       loopback ✓
```

`devd switch` = update proxy target. ~20ms. No disruption.
