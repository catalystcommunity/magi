#!/usr/bin/env bash
# Build the kernel of Magi task VMs (Cloud Hypervisor guests) in a
# container, for x86_64 (vmlinux, PVH boot) and arm64 (Image):
#   tools/vm/build-kernel.sh [x86_64|arm64 ...]     (default: both)
#   tools/vm/build-kernel.sh --install [USER@HOST]
# Writes .build/vm/vmlinux-x86_64, .build/vm/Image-arm64, the config of
# each, and SHA256SUMS. The options are in kernel.config and
# kernel-<arch>.config. The kernel source is checked against its SHA-256.
# --install copies the kernel for the machine (this one, or HOST over SSH)
# to ~/.local/share/magi-runner-tools/vm/vmlinux, the path that the runner
# configuration uses (sandbox.vm.kernel), and checks it there. It builds
# the kernel first when it is missing.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
if [[ ${1:-} == --install ]]; then
  host=${2:-}
  run() { if [[ -n $host ]]; then ssh "$host" "$@"; else bash -c "$*"; fi; }
  case $(run uname -m) in
    x86_64) name=vmlinux-x86_64 ;;
    aarch64) name=Image-arm64 ;;
    *) echo "no task VM kernel for $(run uname -m)" >&2; exit 1 ;;
  esac
  [[ -f $root/.build/vm/$name ]] || "$0" "${name#*-}"
  want=$(cd "$root/.build/vm" && grep " $name\$" SHA256SUMS | cut -d' ' -f1)
  [[ -n $want && $(sha256sum "$root/.build/vm/$name" | cut -d' ' -f1) == "$want" ]] || { echo "$name does not agree with SHA256SUMS" >&2; exit 1; }
  dst=.local/share/magi-runner-tools/vm
  run "mkdir -p ~/$dst"
  if [[ -n $host ]]; then scp -q "$root/.build/vm/$name" "$host:$dst/vmlinux.new"; else cp "$root/.build/vm/$name" ~/$dst/vmlinux.new; fi
  run "cd ~/$dst && echo '$want  vmlinux.new' | sha256sum -c --quiet && mv vmlinux.new vmlinux && echo installed ~/$dst/vmlinux"
  exit 0
fi
KVER=${KVER:-6.18.60}
KSHA=${KSHA:-3c2f44ef6acc021a7ad444d9a2e6ec1ef488ff09368793d0004a6080f69d621e}
IMAGE=${IMAGE:-debian:trixie@sha256:2dc419c6d7fe3dad0a2f9fd469c34cc90b0f9ba642d8fde837fe3d7d34a0e7f1}
out=$root/.build/vm
mkdir -p "$out/src"
arches=("$@")
[[ ${#arches[@]} -eq 0 ]] && arches=(x86_64 arm64)
tar=$out/src/linux-$KVER.tar.xz
if [[ ! -f $tar ]] || [[ $(sha256sum "$tar" | cut -d' ' -f1) != "$KSHA" ]]; then
  curl -fsSL -o "$tar.part" "https://cdn.kernel.org/pub/linux/kernel/v${KVER%%.*}.x/linux-$KVER.tar.xz"
  [[ $(sha256sum "$tar.part" | cut -d' ' -f1) == "$KSHA" ]] || { echo "checksum mismatch for linux-$KVER" >&2; exit 1; }
  mv "$tar.part" "$tar"
fi
docker run --rm -u 0 -v "$out:/out" -v "$root/tools/vm:/cfg:ro" \
  -e KVER="$KVER" -e ARCHES="${arches[*]}" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" "$IMAGE" bash -euo pipefail -c '
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends build-essential bc flex bison libelf-dev libssl-dev python3 cpio xz-utils gcc-aarch64-linux-gnu >/dev/null
for arch in $ARCHES; do
  w=/work/$arch; mkdir -p $w; tar -C $w --strip-components=1 -xJf /out/src/linux-$KVER.tar.xz
  cd $w
  case $arch in
    x86_64) mk=(make ARCH=x86_64); target=vmlinux; file=vmlinux; name=vmlinux-x86_64 ;;
    arm64) mk=(make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu-); target=Image; file=arch/arm64/boot/Image; name=Image-arm64 ;;
  esac
  "${mk[@]}" -s defconfig kvm_guest.config
  frags=(/cfg/kernel.config); [[ -f /cfg/kernel-$arch.config ]] && frags+=(/cfg/kernel-$arch.config)
  scripts/kconfig/merge_config.sh -m -O . .config "${frags[@]}" >/dev/null
  "${mk[@]}" -s olddefconfig
  bad=0
  while IFS= read -r l; do
    [[ $l == CONFIG_* ]] || continue
    grep -qx "$l" .config || { echo "$arch: $l is not set" >&2; bad=1; }
  done < <(cat "${frags[@]}")
  [[ $bad == 0 ]] || exit 1
  "${mk[@]}" -s -j"$(nproc)" $target
  cp $file /out/$name; cp .config /out/$name.config
done
cd /out && sha256sum $(ls vmlinux-x86_64 Image-arm64 2>/dev/null) >SHA256SUMS
chown -R $HOST_UID:$HOST_GID /out
'
cat "$out/SHA256SUMS"
