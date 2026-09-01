# devd Product Benchmark

`benchmark.sh` is the repeatable, user-visible performance gate for devd. It is
not a leaderboard and it is not intended to encourage optimizing a metric at
the expense of correctness. Its question is narrower:

> Does devd still feel immediate enough, and remain light enough, for an
> interactive local Linux development workspace?

The storage data path has a separate controlled benchmark in
[Experiment 13](experiments/exp13-ext4-disk-performance.md). Density methodology
and the original resource floor are recorded in
[Experiment 9](experiments/exp9-density.md).

## Product stories measured

| Story | Measurement boundary | Default acceptance |
|---|---|---:|
| Start a fresh workspace from a prepared image | `devd run` invocation → authenticated SSH succeeds | p95 ≤ 1,000 ms |
| Return to stopped work | `devd start` invocation → authenticated SSH succeeds | p95 ≤ 1,000 ms |
| Branch an experiment | `devd fork` invocation → child authenticated SSH succeeds | p95 ≤ 1,000 ms |
| Change which workspace serves a shared port | `devd switch` invocation → first correct HTTP response | p95 ≤ 250 ms |
| Leave a small set of workspaces idle | Host RSS divided by running VMM count | ≤ 256 MiB/VM |

The one-second lifecycle boundary comes from the productization criterion in
Experiment 12: a cached workspace should be ready before an interactive pause
becomes disruptive. The switch boundary is deliberately looser than the
measured tens of milliseconds, but remains below a perceptible workflow delay.
The RSS boundary keeps ten idle workspaces below roughly 2.5 GiB while retaining
a separate kernel per workspace.

These are make-or-break envelopes, not optimization targets. A change from 320
to 280 ms is not inherently valuable. A regression beyond one second demands
an explanation or an explicit product decision.

## Method

The script:

1. Builds the complete bundle unless `SKIP_BUILD=1` is set.
2. Creates isolated temporary devd state and an isolated host project.
3. Resolves and prepares the selected image once. This first run is reported
   but excluded from hot-path thresholds because network and OCI conversion are
   separate user experiences.
4. Runs each lifecycle operation serially for `RUNS` iterations (default 10).
5. Uses actual CLI wall time and waits for authenticated SSH, rather than merely
   observing an open TCP port.
6. Creates two real HTTP workspaces on one automatically managed port, warms
   both SSH tunnels, then times switch-to-first-correct-response.
7. Reads RSS from the two idle `devd-vm` processes.
8. Reports median and nearest-rank p95, evaluates the acceptance limits, and
   exits nonzero if any limit fails.

The benchmark does not clear macOS host caches or attempt raw-media isolation.
That models repeated work on a developer laptop. Run it while the host is
otherwise idle, on AC power, and with the same image digest when comparing
changes. Record hardware, OS, libkrun, firmware, image digest, and thermal/power
conditions for release baselines.

The shell uses `python3` monotonic timestamps around each command. Process-launch
overhead from obtaining the ending timestamp is included consistently and is
small relative to lifecycle limits; this intentionally favors honest user wall
time over internal instrumentation.

## Running

Full run, including cold image preparation in a fresh cache:

```bash
bash benchmark.sh
```

Fast development run reusing immutable image templates, while keeping all
workspace and database state isolated:

```bash
DEVD_TEST_IMAGE_CACHE="$HOME/.devd/images" SKIP_BUILD=1 RUNS=3 \
  bash benchmark.sh
```

Write the Markdown result while still printing it:

```bash
REPORT=/tmp/devd-benchmark.md RUNS=20 bash benchmark.sh
```

Useful controls:

- `IMAGE=<oci-reference>` — image under test
- `RUNS=<n>` — timed iterations
- `PORT=<n>` — fixed declared test port
- `KEEP=1` — retain failed benchmark state for diagnosis
- `*_P95_MAX_MS` and `RSS_PER_VM_MAX_MIB` — alternate profile thresholds;
  changing these must be documented rather than used to make a regression pass

## Baseline

Apple M5, macOS 26.5.1, APFS, libkrun 1.19.4,
`docker.io/nicolaka/netshoot`, 10 iterations, warm immutable image cache,
2026-08-31:

| User-visible operation | p50 | p95 | Gate |
|---|---:|---:|:---:|
| Cached run → SSH | 316.6 ms | 422.8 ms | PASS |
| Stopped start → SSH | 276.3 ms | 277.5 ms | PASS |
| Fork → SSH | 294.8 ms | 509.7 ms | PASS |
| Warm switch → response | 35.1 ms | 39.6 ms | PASS |
| Idle RSS per VM | 206.5 MiB | 206.5 MiB | PASS |

The image preparation plus first boot was 462.5 ms because the immutable ext4
template already existed. A true cold conversion is expected to be several
seconds and is reported separately rather than compared with cached lifecycle
operations.
