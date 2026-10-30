#!/usr/bin/env bash
# Install or update a Magi runner on this machine for one Magi installation.
#   tools/install-runner.sh MAGI_HOST [RUNNER_NAME]
# MAGI_HOST is the public host of the installation, for example
# magi.example.com. RUNNER_NAME defaults to the short host name of this
# machine. It builds magi-runner, copies the agent harness, and writes a
# configuration file if there is none. It does not make a token and does
# not install the service; it prints the next commands.
set -euo pipefail
host=${1:?usage: tools/install-runner.sh MAGI_HOST [RUNNER_NAME]}
name=${2:-$(hostname -s)}
[[ $host =~ ^[a-z0-9.-]+$ ]] || { echo "bad host $host" >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
conf=$HOME/.config/magi-runner
state=$HOME/.local/share/magi-runner/$host
mkdir -p "$conf" "$state" "$HOME/.local/bin"
chmod 700 "$conf" "$state"

(cd "$root/magi" && GOWORK=off CGO_ENABLED=0 go build -trimpath -o "$HOME/.local/bin/magi-runner.new" ./cmd/magi-runner)
mv "$HOME/.local/bin/magi-runner.new" "$HOME/.local/bin/magi-runner"

# The agent harness runs from a copy, never from this checkout: a task must
# not change the program that runs it.
rm -rf "$state/agent.new"
mkdir -p "$state/agent.new"
cp -r "$root/primitives/agent/magi_agent" "$state/agent.new/"
cp -r "$root"/clients/python/magi_* "$state/agent.new/"  # magi_rpc.py and the client packages
find "$state/agent.new" -name __pycache__ -type d -prune -exec rm -rf {} +
rm -rf "$state/agent"
mv "$state/agent.new" "$state/agent"

cfg=$conf/$host.json
if [[ ! -f $cfg ]]; then
  upstream=$(git -C "$root" remote get-url origin 2>/dev/null || true)
  cat > "$cfg" <<JSON
{
  "server": "https://$host",
  "name": "$name",
  "token_file": "~/.config/magi-runner/$host.token",
  "state_dir": "~/.local/share/magi-runner/$host",
  "repos": {"magi": {"upstream": "$upstream", "default_branch": "main", "build": true}},
  "agent": {"python": "python3", "path": "~/.local/share/magi-runner/$host/agent"},
  "sandbox": {
    "shared": ["~/.cache/magi-runner/$host/gomod"],
    "env": {"GOMODCACHE": "~/.cache/magi-runner/$host/gomod", "GOTOOLCHAIN": "local"}
  }
}
JSON
  chmod 600 "$cfg"
  echo "wrote $cfg"
fi
echo "installed magi-runner for $host (runner $name)"
[[ -f $conf/$host.token ]] || echo "next: put a runner token (subject runner:$name, scope magi:runner) in $conf/$host.token (mode 600)"
cat <<TXT
next, once:
  cp $root/deploy/runner/magi-runner@.service ~/.config/systemd/user/
  systemctl --user daemon-reload
  systemctl --user enable --now magi-runner@$host
after an update:
  systemctl --user restart magi-runner@$host
TXT
