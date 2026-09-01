# Contributing to devd

## Prerequisites

- macOS (Apple Silicon) or Linux
- Buildah, e2fsprogs, libkrun, and libkrunfw — provided by Nix on Linux; on macOS run `install-runtime` inside the devenv shell

## Dev Environment Setup

devd uses [devenv](https://devenv.sh) to manage the development toolchain (Go, linters, git hooks).

Follow the [devenv Getting Started guide](https://devenv.sh/getting-started/) to install Nix and devenv. Then:

```bash
cd devd
devenv shell
```

If you have [direnv](https://direnv.net/) installed, the environment activates automatically when you `cd` into the repo (run `direnv allow` the first time).

## Available Commands

Inside the devenv shell:

| Command | What it does |
|---------|--------------|
| `build` | Build `bin/devd`, `bin/devd-vm`, and `bin/devd-image-helper` |
| `test`  | `go test ./...` |
| `lint`  | `golangci-lint run` |
| `check` | gofmt + vet + lint + test (use before `jj commit`) |
| `setup` | `go mod download` |
| `clean` | Remove `bin/` |
| `install-runtime` | Install Buildah, e2fsprogs, libkrun, and firmware |
| `scripts/package-release <version>` | Build and verify a host-native release archive |

## Project Structure

```
cmd/devd/          CLI entrypoint (cobra)
internal/
  cli/             Command implementations
  db/              SQLite state layer
  storage/         OCI-to-ext4 cache and reflink clone lifecycle
  vm/              devd-vm wrapper and generic guest init
cmd/devd-vm/       separately linked libkrun companion
cmd/devd-image-helper/ static Linux metadata-preserving copy helper
  ssh/             SSH keypair and ~/.ssh/config management
  proxy/           Automatic declared-port pre-emption and routing
  config/          devcontainer.json parsing
experiments/       Networking experiments validating the proxy architecture
```

## Git Hooks / jj Workflow

Pre-commit hooks (gofmt, govet, golangci-lint) are configured via devenv's git-hooks integration. These fire on `git commit`, which jj uses internally.

However, `jj commit` does **not** always trigger git pre-commit hooks reliably. Run `check` manually before committing:

```bash
check
jj commit -m "your message"
```

The `check` script runs the same validations as the git hooks plus `go test`.

## Code Style

- `gofmt` is the formatter. No exceptions.
- Keep packages small and focused. One responsibility per package.
- Keep the Go CLI CGO-free. Shell out to the bundled `devd-vm` companion for VM operations.
- User workspaces are ext4-only. Directory-root mode is confined to one-time image conversion.
- Use `modernc.org/sqlite` for the database (pure Go, no CGO).

## Testing

```bash
test                    # go test ./...
check                   # gofmt + vet + lint + unit tests
bash test-functional.sh # hardware-backed end-to-end product regression
bash benchmark.sh       # user-visible performance acceptance benchmark
```

The functional suite uses an isolated temporary state directory and exercises
run/start/stop/fork, persistent disk state, the default project mount, fresh
fork identity, automatic shared-port routing, switch, and cleanup. It performs
a cold image conversion by default. During development, reuse an existing
immutable cache without reusing workspace state:

```bash
DEVD_TEST_IMAGE_CACHE="$HOME/.devd/images" SKIP_BUILD=1 bash test-functional.sh
```

See [BENCHMARK.md](BENCHMARK.md) for benchmark boundaries, controls, acceptance
rationale, and the current baseline. For architecture experiments, see
`experiments/`. Each experiment includes a Markdown method, acceptance criteria,
results, and conclusion.
