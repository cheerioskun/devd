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
  ssh/             CA, certificates, ~/.ssh/config management
  proxy/           Port pre-emption and proxy daemon (Phase 1b)
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
test     # go test ./...
check    # full gate: gofmt + vet + lint + test
```

For experiments (networking/proxy validation), see the `experiments/` directory. Each experiment has a markdown doc explaining what it tests and the results.
