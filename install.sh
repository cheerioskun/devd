#!/usr/bin/env bash
# Install the released Apple Silicon bundle and its Homebrew runtime dependencies.
# Usage: curl -fsSL https://raw.githubusercontent.com/cheerioskun/devd/main/install.sh | bash

set -euo pipefail

repo=${DEVD_REPOSITORY:-cheerioskun/devd}
version=${DEVD_VERSION:-}

[[ $(uname -s) == Darwin && $(uname -m) == arm64 ]] || {
  echo "ERROR: the binary installer currently supports Apple Silicon macOS only" >&2
  exit 1
}
command -v brew >/dev/null 2>&1 || {
  echo "ERROR: Homebrew is required: https://brew.sh" >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || { echo "ERROR: curl is required" >&2; exit 1; }

if [[ -z "$version" ]]; then
  release_json=$(curl -fsSL -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/$repo/releases/latest")
  version=$(printf '%s\n' "$release_json" | sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' | head -1)
  [[ -n "$version" ]] || { echo "ERROR: could not determine the latest release" >&2; exit 1; }
fi

printf 'Installing devd %s\n' "$version"
brew tap libkrun/krun
if brew help trust >/dev/null 2>&1; then
  brew trust libkrun/krun
fi
if ! brew list --versions libkrun/krun/libkrunfw >/dev/null 2>&1; then
  # Work around the tap occasionally inferring a nonexistent firmware bottle.
  brew install --build-from-source libkrun/krun/libkrunfw
fi
brew install libkrun/krun/libkrun libkrun/krun/buildah e2fsprogs

name="devd_${version}_Darwin_arm64.tar.gz"
base_url="https://github.com/$repo/releases/download/$version"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/devd-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
curl -fL --retry 3 -o "$tmp/$name" "$base_url/$name"
curl -fL --retry 3 -o "$tmp/$name.sha256" "$base_url/$name.sha256"
(
  cd "$tmp"
  shasum -a 256 -c "$name.sha256"
  tar -xzf "$name"
)

install_dir=${DEVD_INSTALL_DIR:-"$(brew --prefix)/bin"}
mkdir -p "$install_dir"
for binary in devd devd-vm devd-image-helper; do
  install -m 0755 "$tmp/$binary" "$install_dir/$binary"
done

"$install_dir/devd" version
cat <<EOF

Installed the complete devd bundle in $install_dir.
Try:
  devd run nicolaka/netshoot -n my-linux
  devd ssh my-linux
EOF
