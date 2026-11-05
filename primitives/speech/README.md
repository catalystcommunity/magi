# speech

## Purpose

The speech primitive changes speech to text and text to speech. It keeps
one kind of record: the household lexicon. Any authenticated caller can use
the service and read the lexicon.

The Go service (`cmd/speech`) implements `contracts/speech.csil`. It sends the
work to two engine processes over HTTP. You can replace an engine with a
different process that serves the same engine API.

## Engines

| Engine | Model | Model license | Default port | Threads | Device |
| --- | --- | --- | --- | --- | --- |
| STT | Parakeet TDT 0.6B v3 INT8 (sherpa-onnx) | CC-BY-4.0 (NVIDIA); sherpa-onnx is Apache-2.0 | 8092 | 2 | CPU |
| TTS | Kokoro 82M | Apache-2.0 | 8093 | 3 | GPU if torch sees one, else CPU |
| turn | Silero VAD 5.1.2 and the turn classifier (MiniLM-L12, fine-tuned, int8) | MIT (Silero), Apache-2.0 (MiniLM) | 8095 | 2 | CPU |

CC-BY-4.0 lets you use, change, and share the model, also for commercial
work. You must give attribution to NVIDIA when you distribute the model.
Magi does not include the model files. The engine host downloads them.

The two models use different Python environments. Thus each model has its
own process. `engine/server.py --engine stt|tts` is the program for both.

- The engine loads the model one time. It checks the model files against
  `engine/model-pins.json` before it loads them.
- One lock in each engine lets only one inference run at a time.
- The engines read 16 kHz 16-bit mono WAV directly. For other audio, they
  use `ffmpeg`. They accept WAV, WebM, Ogg, MP3, FLAC, and other formats that `ffmpeg` can read.
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
| `POST /synthesize` | TTS | `{"text", ..., "timings": true}` | `{"audio" (base64 WAV), "media_type", "words": [[word, start_ms, end_ms]], "audio_ms", ...}` |
| `GET /voices` | TTS | none | `{"voices": [{"id", "name", "language", "description"}]}` |
| `POST /detect` | detect | `{"texts": [...]}` (at most 64 texts, 256 KiB) | `{"scores": [...], "windows", "engine", "elapsed_ms"}` |
| `POST /vad` | turn | audio bytes | `{"speech_ms", "audio_ms", "segments": [[start_ms, end_ms]], "engine", "elapsed_ms"}` |
| `POST /turn` | turn | `{"items": [{"context", "text"}]}` (at most 32) | `{"results": [{"ack", "turn", "unclear"}], "engine", "elapsed_ms"}` |

The engine `detect` is not speech. It is the prompt injection detector of
Magi (ProtectAI deberta v3 v2, Apache-2.0, ONNX on the CPU; pins in
`model-pins.json`). It uses the same host program and token because the
same host runs it. The score of a text is the INJECTION probability of its
worst window of 512 tokens. Magi calls it directly (`MAGI_DETECTOR_URL`);
the speech service does not use it. Environment: Python 3.13 with
onnxruntime 1.31.0 (MIT), tokenizers 0.23.3 (Apache-2.0), and numpy 2.5.4,
in `~/.local/share/magi/speech/installs/detect/venv`; the model is in
`.../detect/models/protectai-v2`.

### Turn engine

The engine `turn` helps Magi decide what to do when the person speaks while
the persona speaks (`docs/speech-interaction.md`, 2.3.1, step 2).

- `/vad` finds speech in a short clip with Silero VAD (32 ms chunks at
  16 kHz). Speech starts at probability 0.5 and stops after 100 ms below
  0.35. A segment shorter than 90 ms is noise. Thus a click, a cough, a
  fan, or music gives no speech, but a TV voice is speech.
- `/turn` classifies the transcript of the person's speech (`text`, often
  only the first words) with the last words of the persona (`context`):
  - `ack`: a backchannel or a reaction ("mm-hm", "wow okay", "oh no").
    The persona continues.
  - `turn`: the person takes the turn: a stop, a question, a correction,
    a new topic, or an answer to a question that the persona asked
    ("yes" after "shall I send it?"). The persona stops.
  - `unclear`: too few words ("so", "um"). Wait for more words.

  The engine makes the text lower case and removes punctuation, as in
  training.

The classifier is our own model: `engine/turn/` has the data generator
(`data.py`), a separate held-out set with other words and topics
(`heldout.py`), and the training script (`train.py`). The model files are
not in the repository. Make them again with `train.py` (a GPU is useful,
not necessary). `model-pins.json` has the hashes of the files that the
engine uses. Put them on the host:

```text
~/.local/share/magi/speech/installs/turn/models/silero_vad.onnx
~/.local/share/magi/speech/installs/turn/models/turn/{model.onnx,tokenizer.json,config.json}
```

Results of `magi-turn-minilm-l12-v1` (int8), 2026-11-04. "Turn as ack" is
the bad error (the persona does not stop for a real turn); "ack as turn" only
stops the persona:

| Set | Items | Turn as ack | Ack as turn | Accuracy |
| --- | --- | --- | --- | --- |
| `heldout.py` (written before training) | 144 | 0 of 74 | 6 of 54 | 95.8 % |
| `heldout2.py` (written after the first model; three of its error kinds then went into `data.py` as ideas, not phrases) | 161 | 0 of 96 | 4 of 50 | 96.9 % |

With a decision of "ack" at P(ack) >= 0.85 and "turn" at P(turn) >= 0.5.
The probabilities do not go above about 0.94 because training uses label
smoothing 0.1. deberta-v3-xsmall with the same data took 1 to 2 turns as
acks on each set. Time: 2.6 ms median for one item on the engine host (2
threads, HTTP included); `/vad` 6 ms for a 2 s clip of 16 kHz 16-bit mono WAV
(other formats go through ffmpeg, about 40 ms).

To evaluate a running engine: `python3 -I engine/turn/eval_engine.py URL
TOKEN_FILE [--set 2]`.

Silero VAD comes from github.com/snakers4/silero-vad, tag v5.1.2,
`src/silero_vad/data/silero_vad.onnx`. The engine uses the Python
environment of `detect` (onnxruntime, tokenizers, numpy).

An error is JSON `{"error": "..."}`. Status 400 or 413 is a bad input. Other
statuses are engine failures. The error does not contain the input text.

## Service API

`SpeechService` (`POST /rpc/SpeechService/{op}`, CBOR):

- `transcribe`: audio and `media_type` to `Transcript` (text, engine,
  `audio_ms`, `elapsed_ms`). The service ignores `language`; Parakeet v3
  finds the language.
- `synthesize`: text, optional `voice`, `speed` (0.5 to 2.0), and `format`
  (only `wav`) to `Audio`. With `timings`, `Audio.words` gives the time of
  each spoken word. Kokoro gives the times of English tokens. The service
  gives each word the place (code point offsets) of a word in the request
  text, not in the spoken text: it matches equal words in order, and a word
  that the spoken text adds (the "to" of "4-6") gets the place of a near
  word (`server/align.go`).
- `list-voices`: optional `language` filter. `en` matches `en-US` and `en-GB`.
- `list-lexicon`, `put-lexicon-entry`, `delete-lexicon-entry`: the household
  lexicon. A change needs the scope `speech:lexicon`. Magi has this scope
  and decides which people can make changes.
- `prepare-text`: the spoken text that `synthesize` makes, with no audio.
- `detect-speech`: audio (`media_type` is `audio/wav` by default) to the
  speech segments (engine `turn`, `/vad`).
- `classify-turn`: at most 32 items of `context` and `text` (each at most
  2000 characters) to the class probabilities (engine `turn`, `/turn`).
  Without `MAGI_SPEECH_TURN_URL`, these two operations return
  `rpc.Unavailable`. `/healthz` does not check this engine.

## Spoken text and the lexicon

Before `synthesize` sends text to the engine, the service makes spoken text
(`server/spoken.go`). `raw` turns this off. The result is in `Audio.spoken`.

1. It removes Markdown: headings, list marks, emphasis, code marks, quote
   marks. A link becomes its text.
2. It says number ranges: `4-6` becomes `4 to 6`. The rule changes `N-M`
   only when N is less than M, the numbers are not part of a longer token
   (a date, a model number, a telephone number), and no score or version
   word is next to it ("won 3-2", "version 1.2-3"). An en dash (`4–6`) is
   always a range. An unclear case does not change.
3. It applies the lexicon. Each entry has:
   - `written`: the word in text. It matches a whole word, not
     case-sensitive. A longer entry wins over a shorter one.
   - `spoken`: text to say instead, for example `readme` for `README`.
   - `ipa`: misaki phonemes, for example `kˈOkəɹO`. The engine gets
     `[spoken or written](/ipa/)`, which Kokoro (misaki) says as given.
   - `heard_as`: forms that speech to text writes for the word, for example
     `Magei`. `transcribe` changes them to `written`, and `Transcript.heard`
     keeps the engine text. Give only forms that are not other real words.

Without a database (`MAGI_DATABASE_URL` not set), the lexicon is empty and
cannot change. Steps 1 and 2 still apply. When the database fails, speech
goes on without the lexicon and logs a warning.

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
| `-turn-url` | `MAGI_SPEECH_TURN_URL` | optional, such as `http://speech.example.local:8095` |
| `-database-url` | `MAGI_DATABASE_URL` | optional: PostgreSQL for the lexicon |
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

The lexicon test needs PostgreSQL. It makes a database, owned by a new role,
and drops both at the end. It skips when `MAGI_TEST_DATABASE_URL` is not set:

```sh
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
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
