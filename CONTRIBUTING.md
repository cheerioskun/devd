# Contributing to devd

## Prerequisites

- macOS (Apple Silicon) or Linux
- [krunvm](https://github.com/containers/krunvm) installed (`brew install krunvm` on macOS)

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
| `clean` | Remove `bin/` |

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

## Git Hooks

Pre-commit hooks run automatically via devenv:

- **gofmt** — format check
- **govet** — static analysis
- **golangci-lint** — comprehensive linting

## Code Style

- `gofmt` is the formatter. No exceptions.
- Keep packages small and focused. One responsibility per package.
- Shell out to `krunvm`/`crun` for VM operations. No CGO.
- Use `modernc.org/sqlite` for the database (pure Go, no CGO).

## Testing

```bash
test
```

For experiments (networking/proxy validation), see the `experiments/` directory. Each experiment has a markdown doc explaining what it tests and the results.
