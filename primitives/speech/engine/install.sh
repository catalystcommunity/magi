#!/usr/bin/env bash
# Install or restart the Magi model engines (speech, and the prompt
# injection detector) on a remote host over SSH.
#
#   install.sh [--provision] [user@]host
#
# --provision first makes the Python environments and gets the models on
# the host (provision.py; MAGI_SPEECH_PROVISION_ARGS adds its options, for
# example "--torch rocm" or "--turn-from DIR"). It installs a pinned uv in
# ~/magi-speech/bin when the host has none. Without --provision, the host
# must already have the environments and models: the paths of the last
# provision (~/.local/share/magi/speech/engines.env) or of the last install,
# else the older layout under ~/.local/share/magi/speech/installs/*/.
# Override with MAGI_SPEECH_STT_PYTHON, MAGI_SPEECH_STT_MODEL,
# MAGI_SPEECH_TTS_PYTHON, MAGI_SPEECH_TTS_MODEL (paths on the host).
# Ports: MAGI_SPEECH_STT_PORT (8092), MAGI_SPEECH_TTS_PORT (8093).
# Threads: MAGI_SPEECH_STT_THREADS (2), MAGI_SPEECH_TTS_THREADS (3).
# TTS device: MAGI_SPEECH_TTS_DEVICE (auto, cpu, gpu; default auto). A GPU needs
# a TTS environment with a CUDA or ROCm torch. MAGI_SPEECH_TTS_ENV adds
# NAME=value words to the TTS engine environment (e.g. HIP_VISIBLE_DEVICES=0).
# A value that is not set keeps the value of the last install (the *.env
# files on the host), so a second run does not move TTS off its GPU.
#
# The prompt injection detector (engine "detect", port MAGI_SPEECH_DETECT_PORT,
# default 8094) is installed when its environment and model are on the host:
# MAGI_SPEECH_DETECT_PYTHON and MAGI_SPEECH_DETECT_MODEL, by default
# ~/.local/share/magi/speech/installs/detect/{venv/bin/python,models/protectai-v2}.
#
# Speech detection and the turn classifier (engine "turn", port
# MAGI_SPEECH_TURN_PORT, default 8095) are installed when their model
# directory is on the host: MAGI_SPEECH_TURN_MODEL, by default
# ~/.local/share/magi/speech/installs/turn/models (silero_vad.onnx and
# turn/). It needs onnxruntime, tokenizers, and numpy: MAGI_SPEECH_TURN_PYTHON,
# by default the detect environment.
#
# Files go to ~/magi-speech on the host. The token is ~/magi-speech/token
# (mode 600). It is made once and never printed. With user lingering enabled,
# systemd --user units run the engines. Otherwise a nohup supervisor loop runs
# them; it does not survive a reboot (run this script again).
set -euo pipefail
provision=0
[ "${1:-}" = --provision ] && { provision=1; shift; }
host=${1:?usage: install.sh [--provision] [user@]host}
here=$(cd "$(dirname "$0")" && pwd)
# ssh joins its words into one command line: quote each value for the remote shell.
q() { printf %q "$1"; }
UV_VERSION=0.12.7
UV_SHA256_x86_64=788f18abea7c5f55d6216e4f5613fd89d4d59b631efeec117b2b07fe72f1da21
UV_SHA256_aarch64=66393193038dd7eb108abd7a218d9cec04ac70ab98242b0720fa94de19223b7c

ssh "$host" 'mkdir -p ~/magi-speech && chmod 700 ~/magi-speech'
scp -q "$here/server.py" "$here/model-pins.json" "$here/supervise.sh" "$host:magi-speech/"

if [ "$provision" = 1 ]; then
  ssh "$host" 'mkdir -p ~/magi-speech/requirements ~/magi-speech/turn ~/magi-speech/bin'
  scp -q "$here/provision.py" "$host:magi-speech/"
  scp -q "$here"/requirements/*.txt "$host:magi-speech/requirements/"
  scp -q "$here"/turn/data.py "$here"/turn/heldout.py "$here"/turn/train.py "$host:magi-speech/turn/"
  ssh "$host" UV_VERSION=$UV_VERSION UV_SHA256_x86_64=$UV_SHA256_x86_64 UV_SHA256_aarch64=$UV_SHA256_aarch64 \
    ARGS=$(q "${MAGI_SPEECH_PROVISION_ARGS:-}") bash -s <<'REMOTE'
set -euo pipefail
cd ~/magi-speech
uv=$(command -v uv || true)
if [ -z "$uv" ]; then
  arch=$(uname -m); want=UV_SHA256_$arch
  [ -n "${!want:-}" ] || { echo "no pinned uv for $arch" >&2; exit 1; }
  name=uv-$arch-unknown-linux-gnu
  curl -fsSL -o bin/uv.tar.gz "https://github.com/astral-sh/uv/releases/download/$UV_VERSION/$name.tar.gz"
  echo "${!want}  bin/uv.tar.gz" | sha256sum -c --quiet
  tar -xzf bin/uv.tar.gz -C bin --strip-components 1 "$name/uv"
  rm bin/uv.tar.gz
  uv=$PWD/bin/uv
fi
# shellcheck disable=SC2086
UV="$uv" "$uv" run --no-project --python 3.13 python -I provision.py $ARGS
REMOTE
fi

ssh "$host" \
  STT_PY=$(q "${MAGI_SPEECH_STT_PYTHON:-}") STT_MODEL=$(q "${MAGI_SPEECH_STT_MODEL:-}") \
  TTS_PY=$(q "${MAGI_SPEECH_TTS_PYTHON:-}") TTS_MODEL=$(q "${MAGI_SPEECH_TTS_MODEL:-}") \
  STT_PORT=$(q "${MAGI_SPEECH_STT_PORT:-8092}") TTS_PORT=$(q "${MAGI_SPEECH_TTS_PORT:-8093}") \
  STT_THREADS=$(q "${MAGI_SPEECH_STT_THREADS:-2}") TTS_THREADS=$(q "${MAGI_SPEECH_TTS_THREADS:-3}") \
  TTS_DEVICE=$(q "${MAGI_SPEECH_TTS_DEVICE:-}") TTS_ENV=$(q "${MAGI_SPEECH_TTS_ENV:-}") \
  DETECT_PY=$(q "${MAGI_SPEECH_DETECT_PYTHON:-}") DETECT_MODEL=$(q "${MAGI_SPEECH_DETECT_MODEL:-}") \
  DETECT_PORT=$(q "${MAGI_SPEECH_DETECT_PORT:-8094}") DETECT_THREADS=$(q "${MAGI_SPEECH_DETECT_THREADS:-2}") \
  TURN_PY=$(q "${MAGI_SPEECH_TURN_PYTHON:-}") TURN_MODEL=$(q "${MAGI_SPEECH_TURN_MODEL:-}") \
  TURN_PORT=$(q "${MAGI_SPEECH_TURN_PORT:-8095}") TURN_THREADS=$(q "${MAGI_SPEECH_TURN_THREADS:-2}") \
  PROVISIONED=$provision bash -s <<'REMOTE'
set -euo pipefail
cd ~/magi-speech
chmod 755 supervise.sh
first() { for p in "$@"; do [ -e "$p" ] && { echo "$p"; return; }; done; echo "missing: $1" >&2; exit 1; }
# The paths of the last provision: after --provision they come before the
# values of the last install.
provisioned=~/.local/share/magi/speech/engines.env
from_provision() {
  [ -s "$provisioned" ] || return 0
  while IFS='=' read -r k v; do
    [ -n "$k" ] && [ -z "${!k:-}" ] && printf -v "$k" '%s' "$v"
  done < "$provisioned"
  return 0
}
[ "$PROVISIONED" = 1 ] && from_provision
# The value of the last install.
last() { [ -s "$1.env" ] && sed -n "s/^$2=//p" "$1.env" | tr -d '"' || true; }
STT_PY=${STT_PY:-$(last stt PYTHON)}; STT_MODEL=${STT_MODEL:-$(last stt MODEL_DIR)}
TTS_PY=${TTS_PY:-$(last tts PYTHON)}; TTS_MODEL=${TTS_MODEL:-$(last tts MODEL_DIR)}
TTS_DEVICE=${TTS_DEVICE:-$(last tts DEVICE)}; TTS_DEVICE=${TTS_DEVICE:-auto}
TTS_ENV=${TTS_ENV:-$(last tts ENGINE_ENV)}
DETECT_PY=${DETECT_PY:-$(last detect PYTHON)}; DETECT_MODEL=${DETECT_MODEL:-$(last detect MODEL_DIR)}
TURN_PY=${TURN_PY:-$(last turn PYTHON)}; TURN_MODEL=${TURN_MODEL:-$(last turn MODEL_DIR)}
from_provision
STT_PY=${STT_PY:-$(first ~/.local/share/magi/speech/installs/*/stt/venv/bin/python)}
STT_MODEL=${STT_MODEL:-$(first ~/.local/share/magi/speech/installs/*/stt/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8)}
TTS_PY=${TTS_PY:-$(first ~/.local/share/magi/speech/installs/*/tts/venv/bin/python)}
TTS_MODEL=${TTS_MODEL:-$(first ~/.local/share/magi/speech/installs/*/tts/models/kokoro)}
DETECT_PY=${DETECT_PY:-$HOME/.local/share/magi/speech/installs/detect/venv/bin/python}
DETECT_MODEL=${DETECT_MODEL:-$HOME/.local/share/magi/speech/installs/detect/models/protectai-v2}
TURN_PY=${TURN_PY:-$DETECT_PY}
TURN_MODEL=${TURN_MODEL:-$HOME/.local/share/magi/speech/installs/turn/models}
engines="stt tts"
[ -x "$DETECT_PY" ] && [ -d "$DETECT_MODEL" ] && engines="$engines detect"
[ -x "$TURN_PY" ] && [ -d "$TURN_MODEL" ] && engines="$engines turn"
command -v ffmpeg >/dev/null || { echo "ffmpeg is required on the host" >&2; exit 1; }

if [ ! -s token ]; then
  (umask 077; head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > token)
fi
chmod 600 token

cat > stt.env <<EOF
ENGINE=stt
PYTHON=$STT_PY
MODEL_DIR=$STT_MODEL
PORT=$STT_PORT
THREADS=$STT_THREADS
EOF
cat > tts.env <<EOF
ENGINE=tts
PYTHON=$TTS_PY
MODEL_DIR=$TTS_MODEL
PORT=$TTS_PORT
THREADS=$TTS_THREADS
DEVICE=$TTS_DEVICE
ENGINE_ENV="$TTS_ENV"
EOF
cat > detect.env <<EOF
ENGINE=detect
PYTHON=$DETECT_PY
MODEL_DIR=$DETECT_MODEL
PORT=$DETECT_PORT
THREADS=$DETECT_THREADS
EOF
cat > turn.env <<EOF
ENGINE=turn
PYTHON=$TURN_PY
MODEL_DIR=$TURN_MODEL
PORT=$TURN_PORT
THREADS=$TURN_THREADS
EOF

stop_nohup() {
  for e in stt tts detect turn; do
    if [ -s "$e.pid" ]; then kill -- "-$(cat "$e.pid")" 2>/dev/null || true; rm -f "$e.pid"; fi
  done
  sleep 1
}

if loginctl show-user "$USER" 2>/dev/null | grep -q '^Linger=yes'; then
  stop_nohup
  mkdir -p ~/.config/systemd/user
  for e in $engines; do
    cat > ~/.config/systemd/user/magi-speech-$e.service <<EOF
[Unit]
Description=Magi speech engine ($e)
[Service]
ExecStart=%h/magi-speech/supervise.sh %h/magi-speech/$e.env --once
Restart=always
RestartSec=5
[Install]
WantedBy=default.target
EOF
  done
  systemctl --user daemon-reload
  units=$(for e in $engines; do printf 'magi-speech-%s ' "$e"; done)
  systemctl --user enable $units >/dev/null
  systemctl --user restart $units
  echo "mode: systemd --user"
else
  stop_nohup
  for e in $engines; do
    setsid nohup ./supervise.sh "$PWD/$e.env" >>"$e.log" 2>&1 </dev/null &
    echo $! > "$e.pid"
  done
  echo "mode: nohup supervisor (user lingering is off; restart after reboot)"
fi

for e in $engines; do
  port=$(sed -n 's/^PORT=//p' "$e.env")
  for _ in $(seq 120); do
    curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
    sleep 1
  done
  printf '%s: ' "$e"; curl -fsS "http://127.0.0.1:$port/health" || { echo "not ready; see ~/magi-speech/$e.log"; exit 1; }
  echo
done
REMOTE
