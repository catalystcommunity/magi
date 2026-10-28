#!/usr/bin/env bash
# Install or restart the Magi speech engines on a remote host over SSH.
#
#   install.sh [user@]host
#
# The host must already have the Parakeet and Kokoro environments and models.
# By default they are found under ~/.local/share/magi/speech/installs/*/.
# Override with MAGI_SPEECH_STT_PYTHON, MAGI_SPEECH_STT_MODEL,
# MAGI_SPEECH_TTS_PYTHON, MAGI_SPEECH_TTS_MODEL (paths on the host).
# Ports: MAGI_SPEECH_STT_PORT (8092), MAGI_SPEECH_TTS_PORT (8093).
# Threads: MAGI_SPEECH_STT_THREADS (2), MAGI_SPEECH_TTS_THREADS (3).
# TTS device: MAGI_SPEECH_TTS_DEVICE (auto, cpu, gpu; default auto). A GPU needs
# a TTS environment with a CUDA or ROCm torch. MAGI_SPEECH_TTS_ENV adds
# NAME=value words to the TTS engine environment (e.g. HIP_VISIBLE_DEVICES=0).
#
# Files go to ~/magi-speech on the host. The token is ~/magi-speech/token
# (mode 600). It is made once and never printed. With user lingering enabled,
# systemd --user units run the engines. Otherwise a nohup supervisor loop runs
# them; it does not survive a reboot (run this script again).
set -euo pipefail
host=${1:?usage: install.sh [user@]host}
here=$(cd "$(dirname "$0")" && pwd)

ssh "$host" 'mkdir -p ~/magi-speech && chmod 700 ~/magi-speech'
scp -q "$here/server.py" "$here/model-pins.json" "$here/supervise.sh" "$host:magi-speech/"

ssh "$host" \
  STT_PY="${MAGI_SPEECH_STT_PYTHON:-}" STT_MODEL="${MAGI_SPEECH_STT_MODEL:-}" \
  TTS_PY="${MAGI_SPEECH_TTS_PYTHON:-}" TTS_MODEL="${MAGI_SPEECH_TTS_MODEL:-}" \
  STT_PORT="${MAGI_SPEECH_STT_PORT:-8092}" TTS_PORT="${MAGI_SPEECH_TTS_PORT:-8093}" \
  STT_THREADS="${MAGI_SPEECH_STT_THREADS:-2}" TTS_THREADS="${MAGI_SPEECH_TTS_THREADS:-3}" \
  TTS_DEVICE="${MAGI_SPEECH_TTS_DEVICE:-auto}" TTS_ENV="${MAGI_SPEECH_TTS_ENV:-}" \
  bash -s <<'REMOTE'
set -euo pipefail
cd ~/magi-speech
chmod 755 supervise.sh
first() { for p in "$@"; do [ -e "$p" ] && { echo "$p"; return; }; done; echo "missing: $1" >&2; exit 1; }
STT_PY=${STT_PY:-$(first ~/.local/share/magi/speech/installs/*/stt/venv/bin/python)}
STT_MODEL=${STT_MODEL:-$(first ~/.local/share/magi/speech/installs/*/stt/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8)}
TTS_PY=${TTS_PY:-$(first ~/.local/share/magi/speech/installs/*/tts/venv/bin/python)}
TTS_MODEL=${TTS_MODEL:-$(first ~/.local/share/magi/speech/installs/*/tts/models/kokoro)}
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

stop_nohup() {
  for e in stt tts; do
    if [ -s "$e.pid" ]; then kill -- "-$(cat "$e.pid")" 2>/dev/null || true; rm -f "$e.pid"; fi
  done
  sleep 1
}

if loginctl show-user "$USER" 2>/dev/null | grep -q '^Linger=yes'; then
  stop_nohup
  mkdir -p ~/.config/systemd/user
  for e in stt tts; do
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
  systemctl --user enable magi-speech-stt magi-speech-tts >/dev/null
  systemctl --user restart magi-speech-stt magi-speech-tts
  echo "mode: systemd --user"
else
  stop_nohup
  for e in stt tts; do
    setsid nohup ./supervise.sh "$PWD/$e.env" >>"$e.log" 2>&1 </dev/null &
    echo $! > "$e.pid"
  done
  echo "mode: nohup supervisor (user lingering is off; restart after reboot)"
fi

for e in stt tts; do
  port=$(sed -n 's/^PORT=//p' "$e.env")
  for _ in $(seq 120); do
    curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
    sleep 1
  done
  printf '%s: ' "$e"; curl -fsS "http://127.0.0.1:$port/health" || { echo "not ready; see ~/magi-speech/$e.log"; exit 1; }
  echo
done
REMOTE
