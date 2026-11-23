#!/usr/bin/env bash
# Install the toolchains that runner sandboxes use, for the current user,
# without root, into ~/.local/share/magi-runner-tools (TOOLS):
#   go       Go GO_VERSION (go.dev, checked against its SHA-256)
#   rust     Rust stable through rustup (CARGO_HOME and RUSTUP_HOME in TOOLS)
#   python   Python 3.13 (python-build-standalone) when python3 is older than 3.11
#   csilgen  csilgen CSILGEN_VERSION with its generators (GitHub, checked
#            against its SHA-256). A contract change needs it for
#            tools/gen.sh. There is no build for x86_64 macOS. The script
#            skips csilgen there.
#   postgres PostgreSQL PG_VERSION (theseus-rs relocatable builds, checked
#            against their SHA-256) for the database tests of
#            tools/test-db.sh. The server version of Magi is 18.
#   cloud-hypervisor  Cloud Hypervisor CH_VERSION (Linux, static, checked
#            against its SHA-256) for task VMs
#   virtiofsd  for task VMs, when the system has none: the package of the
#            distribution (apt-get download, no root), extracted to TOOLS/vm
# Run it on the runner machine, for example:
#   ssh user@host 'bash -s' < tools/install-runner-tools.sh
# Each tool is installed only when it is missing. The runner configuration
# makes TOOLS read-only in the sandbox and puts its bin directories on PATH.
# A runner configuration that already exists keeps its PATH. Add
# $TOOLS/csilgen/bin and $TOOLS/postgres/bin to sandbox.env.PATH by hand.
set -euo pipefail
GO_VERSION=${GO_VERSION:-1.27.0}
PY_RELEASE=${PY_RELEASE:-20261026}
PY_VERSION=${PY_VERSION:-3.13.18}
CH_VERSION=${CH_VERSION:-v54.0}
CSILGEN_VERSION=${CSILGEN_VERSION:-0.3.0}
PG_VERSION=${PG_VERSION:-18.7.0}
declare -A CH_SHA=(
  [amd64]=4f4f98a3d661a40f3e5272627cc895a1b2e525f2f6bedf5f895f9cd21ba5c65b
  [arm64]=e8a6d9786ea678e739f1a386682846e93742dcb9c7b6e3e0c62e3bda6cff0ef3
)
declare -A CSILGEN_SHA=(
  [x86_64-unknown-linux-gnu]=e447cc78221407f788dac404d089e530e27f2fdee868687e3d0251a4a3c8876f
  [aarch64-unknown-linux-gnu]=7954055f8e7340b78117ae69b0f6719f2eebc48c6fe7a5c9bf065e0c6bcea00a
  [aarch64-apple-darwin]=6288f7ad3ac6cb4e4616a060722139e75f4e25db3b83ca033fb781e7a2b27008
  [generators]=319ee21052bde44282e7e373349717e804626408e3df2f7caefb3e7ebc6bec80
)
declare -A PG_SHA=(
  [x86_64-unknown-linux-gnu]=53ca01952cb581145e4fb16b6b3b675da345a82879f350707b9f65863a0642f4
  [aarch64-unknown-linux-gnu]=6fcf45163aa044fe58597885b0e27c4b1614c53ad088d4d0ebbbfdf5df510eaf
  [aarch64-apple-darwin]=86456c0ed99d2466524eaa074db6580cf7d748b91042e252c83eaca9a9d216e7
  [x86_64-apple-darwin]=9cd649259d6bf4af60a235e772c83853df215b63f4c1967c2eed7f9376aefb89
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

# csilgen writes the code of a contract change (tools/gen.sh). The release
# has no build for x86_64 macOS.
if [[ $os == darwin && $pyarch == x86_64 ]]; then
  echo "csilgen has no x86_64 macOS build: not installed"
elif [[ $("$TOOLS/csilgen/lib/csilgen" --version 2>/dev/null) != "csilgen $CSILGEN_VERSION" ]]; then
  case $os in darwin) triple=$pyarch-apple-darwin ;; linux) triple=$pyarch-unknown-linux-gnu ;; esac
  base="https://github.com/catalystcommunity/csilgen/releases/download/csilgen/v$CSILGEN_VERSION"
  f="csilgen-$CSILGEN_VERSION-$triple.tar.gz"
  curl -fsSL -o "$tmp/csilgen.tgz" "$base/$f"
  [[ $(sha "$tmp/csilgen.tgz") == "${CSILGEN_SHA[$triple]}" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  mkdir -p "$tmp/csilgen" && tar -C "$tmp/csilgen" -xzf "$tmp/csilgen.tgz"
  f="csilgen-generators-$CSILGEN_VERSION.tar.gz"
  curl -fsSL -o "$tmp/generators.tgz" "$base/$f"
  [[ $(sha "$tmp/generators.tgz") == "${CSILGEN_SHA[generators]}" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  rm -rf "$TOOLS/csilgen" && mkdir -p "$TOOLS/csilgen/lib" "$TOOLS/csilgen/generators" "$TOOLS/csilgen/bin"
  install -m 755 "$tmp/csilgen/csilgen" "$TOOLS/csilgen/lib/csilgen"
  tar -C "$TOOLS/csilgen/generators" -xzf "$tmp/generators.tgz"
  # csilgen reads its generators only from ~/.csilgen/generators. TOOLS is
  # read-only in the sandbox. The wrapper links that path to the generators
  # of this install. It finds them through $0, so TOOLS works at any path.
  cat > "$TOOLS/csilgen/bin/csilgen" <<'WRAP'
#!/bin/sh
set -e
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$HOME/.csilgen"
[ -e "$HOME/.csilgen/generators" ] || ln -s "$dir/../generators" "$HOME/.csilgen/generators"
exec "$dir/../lib/csilgen" "$@"
WRAP
  chmod 755 "$TOOLS/csilgen/bin/csilgen"
  echo "installed $("$TOOLS/csilgen/lib/csilgen" --version)"
fi

# The database tests need a PostgreSQL server (tools/test-db.sh). The
# relocatable builds of theseus-rs need no root and no system libraries.
# postgres --version prints the server version, major.minor only.
if [[ $("$TOOLS/postgres/bin/postgres" --version 2>/dev/null) != "postgres (PostgreSQL) ${PG_VERSION%.*}"* ]]; then
  case $os in darwin) triple=$pyarch-apple-darwin ;; linux) triple=$pyarch-unknown-linux-gnu ;; esac
  base="https://github.com/theseus-rs/postgresql-binaries/releases/download/$PG_VERSION"
  f="postgresql-$PG_VERSION-$triple.tar.gz"
  curl -fsSL -o "$tmp/pg.tgz" "$base/$f"
  [[ $(sha "$tmp/pg.tgz") == "${PG_SHA[$triple]}" ]] || { echo "checksum mismatch for $f" >&2; exit 1; }
  # The archive has one top directory, postgresql-<version>-<triple>.
  # --strip-components puts its content in $TOOLS/postgres, dotfiles too.
  rm -rf "$TOOLS/postgres" && mkdir -p "$TOOLS/postgres" && tar -C "$TOOLS/postgres" --strip-components=1 -xzf "$tmp/pg.tgz"
  echo "installed $("$TOOLS/postgres/bin/postgres" --version)"
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
[[ -x $TOOLS/csilgen/lib/csilgen ]] && "$TOOLS/csilgen/lib/csilgen" --version
[[ -x $TOOLS/postgres/bin/postgres ]] && "$TOOLS/postgres/bin/postgres" --version
[[ $os == linux ]] && "$TOOLS/vm/cloud-hypervisor" --version
