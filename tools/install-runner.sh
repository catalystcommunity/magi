#!/usr/bin/env bash
# Install a Magi runner for one Magi installation, on this machine or on
# another machine over SSH.
#   tools/install-runner.sh MAGI_HOST [options]
#     --name NAME        runner name (default: the short host name of the target)
#     --ssh USER@HOST    install on that machine (default: this machine)
#     --upstream URL     upstream of the magi repository (default: origin of this checkout)
#     --publish          the runner pushes merged changes with its Git credentials
#     --token FILE       copy this runner token (subject runner:NAME, scope
#                        magi:runner) to the target when it has none
#     --service          install and start the service (systemd user unit on
#                        Linux, launchd daemon on macOS; macOS needs root over SSH)
#
# It builds the runner package for the target platform from this checkout,
# installs it as the first release, and installs the launcher in
# ~/.local/bin/magi-runner. The launcher supervises the release: Magi
# updates the release later, and the launcher rolls back a failed update.
# It writes a configuration file only when there is none.
#
# Toolchains for the sandbox: run tools/install-runner-tools.sh on the
# target first. The configuration uses them when they are there.
set -euo pipefail
usage() { sed -n 2,22p "$0" | sed 's/^# \{0,1\}//'; exit 2; }
host=${1:-}; [[ -n $host && $host != -* ]] || usage; shift
name="" ssh_target="" upstream="" publish=false token="" service=false
while (( $# )); do
  case $1 in
    --name) name=$2; shift 2 ;;
    --ssh) ssh_target=$2; shift 2 ;;
    --upstream) upstream=$2; shift 2 ;;
    --publish) publish=true; shift ;;
    --token) token=$2; shift 2 ;;
    --service) service=true; shift ;;
    *) usage ;;
  esac
done
[[ $host =~ ^[a-z0-9.-]+$ ]] || { echo "bad host $host" >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
on() { if [[ -n $ssh_target ]]; then ssh -o BatchMode=yes "$ssh_target" "$@"; else bash -c "$*"; fi; }
put() { if [[ -n $ssh_target ]]; then scp -q -o BatchMode=yes "$1" "$ssh_target:$2"; else cp "$1" "$2"; fi; }

read -r tos tarch tname < <(on 'echo "$(uname -s) $(uname -m) $(hostname -s)"')
os=$(tr '[:upper:]' '[:lower:]' <<<"$tos")
case $tarch in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported machine $tarch" >&2; exit 1 ;; esac
name=${name:-$(tr '[:upper:]' '[:lower:]' <<<"$tname")}
[[ $name =~ ^[a-z0-9-]+$ ]] || { echo "bad runner name $name" >&2; exit 2; }
upstream=${upstream:-$(git -C "$root" remote get-url origin 2>/dev/null || true)}

version=$(git -C "$root" rev-parse --short=12 HEAD)
[[ -z $(git -C "$root" status --porcelain) ]] || version="$version-dev$(date -u +%Y%m%d%H%M%S)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd "$root/magi" && GOWORK=off go run ./cmd/runner-package -src .. -out "$tmp" -platforms "$os/$arch" >/dev/null)
pkg=$tmp/magi-runner-$os-$arch.tar.gz
tar -xzf "$pkg" -C "$tmp" magi-runner

rtmp=$(on 'mktemp -d')
put "$pkg" "$rtmp/pkg.tar.gz"
put "$tmp/magi-runner" "$rtmp/magi-runner"
if [[ -n $token ]]; then
  on "test -f ~/.config/magi-runner/$host.token" || { put "$token" "$rtmp/token"; on "mkdir -p ~/.config/magi-runner && chmod 700 ~/.config/magi-runner && install -m 600 $rtmp/token ~/.config/magi-runner/$host.token"; }
fi
on "HOST=$host NAME=$name UPSTREAM=$upstream PUBLISH=$publish VERSION=$version RTMP=$rtmp bash -s" <<'SH'
set -euo pipefail
conf=$HOME/.config/magi-runner state=$HOME/.local/share/magi-runner/$HOST cache=$HOME/.cache/magi-runner/$HOST
tools=$HOME/.local/share/magi-runner-tools
mkdir -p "$conf" "$state" "$cache" "$HOME/.local/bin"
chmod 700 "$conf" "$state"
install -m 755 "$RTMP/magi-runner" "$HOME/.local/bin/magi-runner.new"
mv "$HOME/.local/bin/magi-runner.new" "$HOME/.local/bin/magi-runner"
cfg=$conf/$HOST.json
if [[ ! -f $cfg ]]; then
  path=/usr/local/bin:/usr/bin:/bin python=python3 ro=""
  [[ -x $tools/python/bin/python3 ]] && { path=$tools/python/bin:$path; python=$tools/python/bin/python3; }
  [[ -d $tools/cargo ]] && path=$tools/cargo/bin:$path
  [[ -d $tools/go ]] && path=$tools/go/bin:$path
  [[ -d $tools ]] && ro="\"$tools\""
  cat > "$cfg" <<JSON
{
  "server": "https://$HOST",
  "name": "$NAME",
  "token_file": "$conf/$HOST.token",
  "state_dir": "$state",
  "repos": {"magi": {"upstream": "$UPSTREAM", "default_branch": "main", "build": true, "publish": $PUBLISH}},
  "author": {"name": "Magi", "email": "magi@$HOST"},
  "apprentice": {"python": "$python"},
  "sandbox": {
    "read_only": [$ro],
    "shared": ["$cache/gomod", "$cache/gobuild", "$cache/cargo", "$cache/cargo-target"],
    "env": {"PATH": "$path", "GOMODCACHE": "$cache/gomod", "GOCACHE": "$cache/gobuild", "GOTOOLCHAIN": "local",
            "RUSTUP_HOME": "$tools/rustup", "CARGO_HOME": "$cache/cargo", "CARGO_TARGET_DIR": "$cache/cargo-target"}
  }
}
JSON
  chmod 600 "$cfg"
  echo "wrote $cfg"
fi
"$HOME/.local/bin/magi-runner" install -config "$cfg" -version "$VERSION" "$RTMP/pkg.tar.gz"
rm -rf "$RTMP"
echo "installed release $VERSION of runner $NAME for $HOST"
[[ -f $conf/$HOST.token ]] || echo "missing: a runner token in $conf/$HOST.token (mode 600)"
SH

if $service; then
  if [[ $os == linux ]]; then
    put "$root/deploy/runner/magi-runner@.service" /tmp/magi-runner@.service
    on "mkdir -p ~/.config/systemd/user && mv /tmp/magi-runner@.service ~/.config/systemd/user/ && systemctl --user daemon-reload && systemctl --user enable magi-runner@$host && systemctl --user restart magi-runner@$host"
  else
    user=${ssh_target%@*}
    [[ -n $ssh_target ]] || { echo "on macOS, install the service over SSH as root" >&2; exit 1; }
    label=community.catalyst.magi-runner.$host
    uhome=$(on 'echo $HOME')
    sed -e "s|@LABEL@|$label|g" -e "s|@USER@|$user|g" -e "s|@HOME@|$uhome|g" -e "s|@HOST@|$host|g" \
      "$root/deploy/runner/magi-runner.plist" > "$tmp/$label.plist"
    host_only=${ssh_target#*@}
    scp -q -o BatchMode=yes "$tmp/$label.plist" "root@$host_only:/Library/LaunchDaemons/$label.plist"
    ssh -o BatchMode=yes "root@$host_only" "chown root:wheel /Library/LaunchDaemons/$label.plist && chmod 644 /Library/LaunchDaemons/$label.plist && (launchctl bootout system/$label 2>/dev/null || true) && launchctl bootstrap system /Library/LaunchDaemons/$label.plist"
  fi
  echo "service started"
else
  echo "service: run again with --service, or start it by hand (see magi/README.md)"
fi
