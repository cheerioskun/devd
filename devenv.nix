{ pkgs, lib, config, inputs, ... }:

{
  # https://devenv.sh/packages/
  packages = [
    pkgs.git
    pkgs.golangci-lint
  ];

  # https://devenv.sh/languages/
  languages.go.enable = true;

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

  # https://devenv.sh/basics/
  enterShell = ''
    echo "devd dev shell"
    echo "  build  — go build -o bin/devd ./cmd/devd"
    echo "  test   — go test ./..."
    echo "  lint   — golangci-lint run"
    echo "  clean  — rm -rf bin/"
    echo ""
    go version
  '';

  # https://devenv.sh/tests/
  enterTest = ''
    go version
    # build should succeed once cmd/devd exists
    # go build ./cmd/devd
  '';

  # https://devenv.sh/git-hooks/
  git-hooks.hooks = {
    gofmt.enable = true;
    govet.enable = true;
    golangci-lint.enable = true;
  };

  # See full reference at https://devenv.sh/reference/options/
}
