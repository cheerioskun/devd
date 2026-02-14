# Experiment 7: Multi-VM Loopback Isolation (No Pre-emption)

**Date:** 2026-02-15  
**krunvm version:** 0.2.6  
**Platform:** macOS ARM64  
**Image:** nicolaka/netshoot

## Question

Without host pre-emption, does each VM's `curl localhost:8080` reach its own server, or the other VM's?

## Method

1. No host listener. Port 8080 is free.
2. Start VM-A: serves `"from-vm-a"` on `:8080`. Runs 5 loopback curls.
3. Start VM-B: serves `"from-vm-b"` on `:8080`. Runs 5 loopback curls.
4. Check host-side: `lsof`, 5 curls.

## Results

**VM-A (first to start):**

| Curl | Result |
|------|--------|
| 1–5 | `"from-vm-a"` — own server (5/5) |

`ss -tlnp`: empty (normal TSI)

**VM-B (second to start, VM-A already owns port on host):**

| Curl | Result |
|------|--------|
| 1–5 | **`"from-vm-a"`** — VM-A's server, not its own (5/5) |

`ss -tlnp`: empty (normal TSI)

**Host:**

| Check | Result |
|-------|--------|
| `lsof -i :8080` | Two krunvm `*:8080 LISTEN` entries |
| 5 curls | `"from-vm-a"` 5/5 (first-to-bind wins) |

## Conclusion

**Without pre-emption, guest loopback is NOT isolated in multi-VM.** VM-B's `curl localhost:8080` goes through TSI to the host, where the kernel routes to VM-A's listener (the first-to-bind). VM-B cannot reach its own server via localhost.

This is why devd pre-empts contested ports: it forces TSI to fall back to real kernel sockets, making each VM's loopback truly local and independent.
