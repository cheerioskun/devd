{ pkgs, lib, config, inputs, ... }:

{
  # https://devenv.sh/packages/
  packages = [
    pkgs.git
    pkgs.golangci-lint
    pkgs.jujutsu
    pkgs.openssh
  ] ++ lib.optionals pkgs.stdenv.hostPlatform.isLinux [
    pkgs.krunvm
  ];

  # https://devenv.sh/languages/
  # Uses pkgs.go from nixpkgs (binary cached). nixpkgs-unstable tracks latest stable.
  languages.go.enable = true;

  # Enforce no-CGO invariant (see SPEC.md: "No CGO. Ever.")
  env.CGO_ENABLED = "0";
  env.GOBIN = "${config.env.DEVENV_ROOT}/bin";

  # https://devenv.sh/scripts/
  scripts.build.exec = ''
    set -euo pipefail
    echo "Building devd..."
    go build -o bin/devd ./cmd/devd
    echo "Built bin/devd"
  '';

  scripts.test.exec = ''
    set -euo pipefail
    echo "Running tests..."
    go test ./...
  '';

  scripts.lint.exec = ''
    set -euo pipefail
    echo "Linting..."
    golangci-lint run ./...
  '';

  scripts.clean.exec = ''
    set -euo pipefail
    rm -rf bin/
    echo "Cleaned."
  '';

  scripts.check.exec = ''
    set -euo pipefail
    echo "Running all checks (gofmt, vet, lint, test)..."
    echo ""

    echo "==> gofmt"
    bad=$(find . -name '*.go' -not -path './.devenv/*' -not -path './vendor/*' | xargs gofmt -l 2>&1) || true
    if [ -n "$bad" ]; then
      echo "FAIL: gofmt found unformatted files:"
      echo "$bad"
      exit 1
    fi
    echo "    ok"
    echo ""

    echo "==> go vet"
    go vet ./...
    echo "    ok"
    echo ""

    echo "==> golangci-lint"
    golangci-lint run ./...
    echo "    ok"
    echo ""

    echo "==> go test"
    go test ./...
    echo "    ok"
    echo ""

    echo "All checks passed."
  '';

  scripts.setup.exec = ''
    set -euo pipefail
    echo "Downloading Go modules..."
    go mod download
    echo "Done."
  '';

  scripts.setup-jj.exec = ''
    set -euo pipefail
    if jj root &>/dev/null; then
      echo "jj is already initialized: $(jj root)"
      exit 0
    fi
    if [ ! -d .git ]; then
      echo "ERROR: .git not found; clone this repo first, then run setup-jj"
      exit 1
    fi

    echo "Initializing colocated jj repo..."
    jj git init --colocate

    if jj bookmark list --remote origin 2>/dev/null | grep -q '^main@origin:'; then
      jj bookmark track main --remote=origin 2>/dev/null || true
    fi

    echo "Done. Use: jj st, jj log -r 'all()'"
  '';

  scripts.install-krunvm.exec = ''
    exec "${config.env.DEVENV_ROOT}/scripts/install-krunvm" "$@"
  '';

  # https://devenv.sh/basics/
  enterShell = ''
    echo "devd dev shell"
    echo "  build  \u2014 go build -o bin/devd ./cmd/devd"
    echo "  test   \u2014 go test ./..."
    echo "  lint   \u2014 golangci-lint run"
    echo "  check  \u2014 gofmt + vet + lint + test (pre-commit substitute for jj)"
    echo "  setup  \u2014 go mod download"
    echo "  setup-jj \u2014 initialize jj after a plain git clone"
    echo "  clean  \u2014 rm -rf bin/"
    echo "  install-krunvm \u2014 install krunvm (macOS: brew, Fedora: dnf)"
    echo ""
    go version
    jj version 2>/dev/null || true

    if [ -d .git ] && [ ! -d .jj ]; then
      echo ""
      echo "INFO this repo uses jj. Run: setup-jj"
    fi

    # Warn if krunvm is not installed (provided by Nix on Linux, manual on macOS)
    if ! command -v krunvm &>/dev/null; then
      echo ""
      echo "WARNING: krunvm not found."
      echo "  devd shells out to krunvm for VM lifecycle."
      echo "  Run: install-krunvm"
      echo "  See: https://github.com/containers/krunvm"
    fi

    # Pre-fetch modules on first entry if go.sum exists but module cache is cold
    if [ -f go.sum ] && ! go list -m all &>/dev/null; then
      echo ""
      echo "Downloading Go modules (first time)..."
      go mod download
    fi
  '';

  # https://devenv.sh/tests/
  enterTest = ''
    echo "Running enterTest..."
    go version
    go build ./cmd/devd
    go vet ./...
    go test ./...
    golangci-lint run ./...
  '';

  # https://devenv.sh/git-hooks/
  git-hooks.hooks = {
    gofmt.enable = true;
    govet.enable = true;
    golangci-lint.enable = true;
  };

  # See full reference at https://devenv.sh/reference/options/
}
