#!/usr/bin/env bash
# Install the toolchains that runner sandboxes use, for the current user,
# without root, into ~/.local/share/magi-runner-tools (TOOLS):
#   go       Go GO_VERSION (go.dev, checked against its SHA-256)
#   rust     Rust stable through rustup (CARGO_HOME and RUSTUP_HOME in TOOLS)
#   python   Python 3.13 (python-build-standalone) when python3 is older than 3.11
#   cloud-hypervisor  Cloud Hypervisor CH_VERSION (Linux, static, checked
#            against its SHA-256) for task VMs
#   virtiofsd  for task VMs, when the system has none: the package of the
#            distribution (apt-get download, no root), extracted to TOOLS/vm
# Run it on the runner machine, for example:
#   ssh user@host 'bash -s' < tools/install-runner-tools.sh
# Each tool is installed only when it is missing. The runner configuration
# makes TOOLS read-only in the sandbox and puts its bin directories on PATH.
set -euo pipefail
GO_VERSION=${GO_VERSION:-1.27.0}
PY_RELEASE=${PY_RELEASE:-20261026}
PY_VERSION=${PY_VERSION:-3.13.18}
CH_VERSION=${CH_VERSION:-v54.0}
declare -A CH_SHA=(
  [amd64]=4f4f98a3d661a40f3e5272627cc895a1b2e525f2f6bedf5f895f9cd21ba5c65b
  [arm64]=e8a6d9786ea678e739f1a386682846e93742dcb9c7b6e3e0c62e3bda6cff0ef3
)
TOOLS=${TOOLS:-$HOME/.local/share/magi-runner-tools}
mkdir -p "$TOOLS"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  x86_64|amd64) arch=amd64; pyarch=x86_64 ;;
  aarch64|arm64) arch=arm64; pyarch=aarch64 ;;
  *) echo "unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
sha() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

if [[ $("$TOOLS/go/bin/go" version 2>/dev/null) != *"go$GO_VERSION "* ]]; then
  f="go$GO_VERSION.$os-$arch.tar.gz"
  want=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -c "import json,sys
for r in json.load(sys.stdin):
  for f in r['files']:
    if f['filename'] == '$f': print(f['sha256'])")
  [[ -n $want ]] || { echo "no checksum for $f" >&2; exit 1; }
  curl -fsSL -o "$tmp/$f" "https://go.dev/dl/$f"
  [[ $(sha "$tmp/$f") == "$want" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  rm -rf "$TOOLS/go" && tar -C "$TOOLS" -xzf "$tmp/$f"
  echo "installed $("$TOOLS/go/bin/go" version)"
fi

export CARGO_HOME=$TOOLS/cargo RUSTUP_HOME=$TOOLS/rustup
if ! "$CARGO_HOME/bin/cargo" --version >/dev/null 2>&1; then
  curl -fsSL -o "$tmp/rustup-init.sh" https://sh.rustup.rs
  sh "$tmp/rustup-init.sh" -y -q --no-modify-path --profile minimal --default-toolchain stable
  echo "installed $("$CARGO_HOME/bin/cargo" --version)"
fi

if ! python3 -c 'import sys; sys.exit(sys.version_info < (3, 11))' 2>/dev/null && ! "$TOOLS/python/bin/python3" --version >/dev/null 2>&1; then
  case $os in darwin) triple=$pyarch-apple-darwin ;; linux) triple=$pyarch-unknown-linux-gnu ;; esac
  f="cpython-$PY_VERSION+$PY_RELEASE-$triple-install_only.tar.gz"
  base="https://github.com/astral-sh/python-build-standalone/releases/download/$PY_RELEASE"
  curl -fsSL -o "$tmp/py.tgz" "$base/${f//+/%2B}"
  want=$(curl -fsSL "$base/SHA256SUMS" | awk -v f="$f" '$2 == f {print $1}')
  [[ -n $want && $(sha "$tmp/py.tgz") == "$want" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  rm -rf "$TOOLS/python" && tar -C "$TOOLS" -xzf "$tmp/py.tgz"
  echo "installed $("$TOOLS/python/bin/python3" --version)"
fi
if [[ $os == linux ]] && [[ $("$TOOLS/vm/cloud-hypervisor" --version 2>/dev/null) != *"${CH_VERSION#v}"* ]]; then
  f=cloud-hypervisor-static; [[ $arch == arm64 ]] && f=$f-aarch64
  curl -fsSL -o "$tmp/ch" "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$CH_VERSION/$f"
  [[ $(sha "$tmp/ch") == "${CH_SHA[$arch]}" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  mkdir -p "$TOOLS/vm" && install -m 755 "$tmp/ch" "$TOOLS/vm/cloud-hypervisor"
  echo "installed $("$TOOLS/vm/cloud-hypervisor" --version)"
fi
sysfsd=$(ls /usr/libexec/virtiofsd /usr/lib/virtiofsd /usr/lib/qemu/virtiofsd 2>/dev/null | head -1 || true)
if [[ $os == linux && -z $sysfsd && ! -x $TOOLS/vm/virtiofsd ]]; then
  if command -v apt-get >/dev/null; then
    (cd "$tmp" && apt-get download -q virtiofsd >/dev/null)
    dpkg-deb -x "$tmp"/virtiofsd_*.deb "$tmp/fsd"
    mkdir -p "$TOOLS/vm" && install -m 755 "$(ls "$tmp"/fsd/usr/libexec/virtiofsd "$tmp"/fsd/usr/lib/virtiofsd 2>/dev/null | head -1)" "$TOOLS/vm/virtiofsd"
    echo "installed virtiofsd $("$TOOLS/vm/virtiofsd" --version)"
  else
    echo "task VMs need virtiofsd: install the virtiofsd package" >&2
  fi
fi
echo "tools in $TOOLS:"
"$TOOLS/go/bin/go" version
"$TOOLS/cargo/bin/cargo" --version
{ "$TOOLS/python/bin/python3" --version 2>/dev/null || python3 --version; }
[[ $os == linux ]] && "$TOOLS/vm/cloud-hypervisor" --version
