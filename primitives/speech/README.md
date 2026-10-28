# speech

## Purpose

The speech primitive changes speech to text and text to speech. It keeps no
records. Any authenticated caller can use it.

The Go service (`cmd/speech`) implements `contracts/speech.csil`. It sends the
work to two engine processes over HTTP. You can replace an engine with a
different process that serves the same engine API.

## Engines

| Engine | Model | Model license | Default port | Threads | Device |
| --- | --- | --- | --- | --- | --- |
| STT | Parakeet TDT 0.6B v3 INT8 (sherpa-onnx) | CC-BY-4.0 (NVIDIA); sherpa-onnx is Apache-2.0 | 8092 | 2 | CPU |
| TTS | Kokoro 82M | Apache-2.0 | 8093 | 3 | GPU if torch sees one, else CPU |

CC-BY-4.0 lets you use, change, and share the model, also for commercial
work. You must give attribution to NVIDIA when you distribute the model.
Magi does not include the model files. The engine host downloads them.

The two models use different Python environments. Thus each model has its
own process. `engine/server.py --engine stt|tts` is the program for both.

- The engine loads the model one time. It checks the model files against
  `engine/model-pins.json` before it loads them.
- One lock in each engine lets only one inference run at a time.
- The STT engine uses `ffmpeg` to decode the audio. It accepts WAV, WebM,
  Ogg, MP3, FLAC, and other formats that `ffmpeg` can read.
- The TTS engine makes 24 kHz mono 16-bit WAV.
- The engines run with `nice 10`.

Voices (12): `af_heart` (default), `af_bella`, `af_nicole`, `af_sarah`,
`af_sky`, `am_fenrir`, `am_michael`, `am_puck` (US English), `bf_emma`,
`bf_isabella`, `bm_george`, `bm_fable` (British English). In the ID, `f` is a
female voice and `m` is a male voice.

### Engine API

All requests, except `/health`, need `Authorization: Bearer <token>`.

| Request | Engine | Body | Response |
| --- | --- | --- | --- |
| `GET /health` | both | none | `{"ok", "engine", "busy"}` |
| `POST /transcribe` | STT | audio bytes | `{"text", "engine", "audio_ms", "elapsed_ms"}` |
| `POST /synthesize` | TTS | `{"text", "voice"?, "speed"?}` | WAV, headers `X-Audio-Ms`, `X-Elapsed-Ms`, `X-Voice`, `X-Engine` |
| `GET /voices` | TTS | none | `{"voices": [{"id", "name", "language", "description"}]}` |

An error is JSON `{"error": "..."}`. Status 400 or 413 is a bad input. Other
statuses are engine failures. The error does not contain the input text.

## Service API

`SpeechService` (`POST /rpc/SpeechService/{op}`, CBOR):

- `transcribe`: audio and `media_type` to `Transcript` (text, engine,
  `audio_ms`, `elapsed_ms`). The service ignores `language`; Parakeet v3
  finds the language.
- `synthesize`: text, optional `voice`, `speed` (0.5 to 2.0), and `format`
  (only `wav`) to `Audio`.
- `list-voices`: optional `language` filter. `en` matches `en-US` and `en-GB`.

Errors:

- `rpc.Invalid` (400): empty input, input over the limits, a media type that
  is not audio, audio that the engine cannot decode, an unknown voice.
- `rpc.Unavailable` (503): the engine is not configured, cannot be reached,
  rejects the engine token, or fails.

`/healthz` returns 503 when one of the engines does not answer `/health`.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | required |
| `-stt-url` | `MAGI_SPEECH_STT_URL` | required, such as `http://speech.example.local:8092` |
| `-tts-url` | `MAGI_SPEECH_TTS_URL` | required, such as `http://speech.example.local:8093` |
| `-engine-token-file` | `MAGI_SPEECH_ENGINE_TOKEN_FILE` | required |

The service reads the engine token one time, at start.

## Deploy the engines

The host must have `ffmpeg` and the installed Parakeet and Kokoro Python
environments and models. Then run:

```sh
primitives/speech/engine/install.sh user@speech.example.local
```

The script does these steps:

1. It copies `server.py`, `model-pins.json`, and `supervise.sh` to
   `~/magi-speech/` on the host.
2. It finds the environments and models under
   `~/.local/share/magi/speech/installs/*/`. To use other paths, set
   `MAGI_SPEECH_{STT,TTS}_{PYTHON,MODEL}`. Set ports and threads with
   `MAGI_SPEECH_{STT,TTS}_{PORT,THREADS}`.
3. It makes `~/magi-speech/token` (mode 600) if the file does not exist. It
   does not show the token.
4. It starts or restarts the two engines and waits for `/health`.

If user lingering is on, systemd user units `magi-speech-stt` and
`magi-speech-tts` run the engines. If lingering is off, a `nohup` supervisor
loop runs each engine and restarts it after a crash. The loop does not
continue after a host restart. Then run `install.sh` again. Logs and PID
files are in `~/magi-speech/`.

### TTS on a GPU

The TTS engine uses a GPU when its Python environment has a CUDA or ROCm
build of PyTorch (BSD-3-Clause; ROCm is MIT and similar licenses). Make a
second environment with the same packages and a GPU torch, then give its
path to `install.sh`:

```sh
MAGI_SPEECH_TTS_PYTHON=/path/on/host/venv/bin/python \
MAGI_SPEECH_TTS_DEVICE=gpu \
MAGI_SPEECH_TTS_ENV=HIP_VISIBLE_DEVICES=0 \
primitives/speech/engine/install.sh user@speech.example.local
```

- `gpu` stops the engine at start when torch sees no GPU. `auto` uses the
  CPU then.
- `/health` shows the device.
- The user must be in the `render` and `video` groups. ROCm wheels have their
  own runtime. The host does not need a system ROCm install.
- If the host also has an integrated GPU, set `HIP_VISIBLE_DEVICES` (ROCm)
  or `CUDA_VISIBLE_DEVICES` to the discrete GPU.
- On ROCm the engine turns off MIOpen. MIOpen fails on the packed LSTMs of
  Kokoro, and its convolutions are slower than the native kernels.

Measured on an RX 7600 (gfx1102, torch 2.9.1+rocm6.4): a 20.8 s reply takes
1.0 s on the GPU, and 3.0 s on 3 CPU threads. STT stays on the CPU: it
transcribes 8.5 s of audio in 0.24 s.

Copy the token to the host of the Go service. Do not show the token or put it
in the repository.

## Tests

Unit tests use a fake engine, the real rpc HTTP transport, and the generated
client:

```sh
cd primitives/speech && go vet ./... && go test ./...
```

Live tests use the real engines. They synthesize a sentence, transcribe it,
and check the word overlap. They also transcribe the clips in `testdata/`:

```sh
MAGI_TEST_SPEECH=1 \
MAGI_SPEECH_STT_URL=http://speech.example.local:8092 \
MAGI_SPEECH_TTS_URL=http://speech.example.local:8093 \
MAGI_SPEECH_ENGINE_TOKEN_FILE=/path/to/token \
go test -count=1 -v -run Live ./server/
```

Add `MAGI_SPEECH_WRITE_TESTDATA=1` to make the clips again. The test uses
local `ffmpeg` with libopus to encode them.

### Test clips

The clips in `testdata/` simulate a user who speaks to Magi. The voice is
`am_michael`. The format is Ogg Opus, 16 kHz mono, 24 kbit/s.

| File | Text |
| --- | --- |
| `hello.opus` | Hello Magi, can you hear me? |
| `calendar.opus` | What is on my calendar today? |
| `remember.opus` | Remember that my work calendar is in the company Google account and my family calendar is in the household Nextcloud. |
| `readme.opus` | Please add a health endpoint summary to the Magi README. |

Kokoro reads "README" as letters. Thus `readme.opus` uses the spoken text
"the Magi readme".

## Limits

- Audio: 16 MiB at most and 300 seconds at most after decoding.
- Text: 4000 characters at most. The text must not be empty.
- Output format: WAV only. There is no streaming.
- One inference at a time for each engine. Other requests wait.
- Parakeet does not give a confidence value. `confidence` is not set.
- The engine token is a shared secret. Engine traffic uses plain HTTP. Use
  the engines only on a trusted network.
- Parakeet sometimes writes "Magi" as "Magei". Kokoro sometimes says "4-6" as
  "four six".
