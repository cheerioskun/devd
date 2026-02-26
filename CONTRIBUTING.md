# Contributing to devd

## Prerequisites

- macOS (Apple Silicon) or Linux
- [krunvm](https://github.com/containers/krunvm) — provided by Nix on Linux; on macOS run `install-krunvm` inside the devenv shell

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
| `build` | `go build -o bin/devd ./cmd/devd` |
| `test`  | `go test ./...` |
| `lint`  | `golangci-lint run` |
| `check` | gofmt + vet + lint + test (use before `jj commit`) |
| `setup` | `go mod download` |
| `clean` | Remove `bin/` |
| `install-krunvm` | Install krunvm (macOS: brew, Fedora: dnf) |

## Project Structure

```
cmd/devd/          CLI entrypoint (cobra)
internal/
  cli/             Command implementations
  db/              SQLite state layer
  vm/              krunvm wrapper (create/start/stop/delete)
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
- Shell out to `krunvm`/`crun` for VM operations. No CGO.
- Use `modernc.org/sqlite` for the database (pure Go, no CGO).

## Testing

```bash
test     # go test ./...
check    # full gate: gofmt + vet + lint + test
```

For experiments (networking/proxy validation), see the `experiments/` directory. Each experiment has a markdown doc explaining what it tests and the results.
