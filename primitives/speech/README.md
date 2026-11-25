# speech

## Purpose

The speech primitive changes speech to text and text to speech, and it
tells if speech is the speech of one person. It keeps two kinds of record:
the household lexicon, and the voice prints of each person. Any
authenticated caller can use the service and read the lexicon. A caller
reads and changes only its own voice prints.

The Go service (`cmd/speech`) implements `contracts/speech.csil`. It sends the
work to two engine processes over HTTP. You can replace an engine with a
different process that serves the same engine API.

## Engines

| Engine | Model | Model license | Default port | Threads | Device |
| --- | --- | --- | --- | --- | --- |
| STT | Parakeet TDT 0.6B v3 INT8 (sherpa-onnx) | CC-BY-4.0 (NVIDIA); sherpa-onnx is Apache-2.0 | 8092 | 2 | CPU |
| TTS | Kokoro 82M | Apache-2.0 | 8093 | 3 | GPU if torch sees one, else CPU |
| turn | Silero VAD 5.1.2 and the turn classifier (MiniLM-L12, fine-tuned, int8) | MIT (Silero), Apache-2.0 (MiniLM) | 8095 | 2 | CPU |
| speaker | 3D-Speaker ERes2Net (English, VoxCeleb, 16 kHz) and VoxLock INT8, both with sherpa-onnx 2.0.1 | Apache-2.0 (both repositories) | 8096 | 4 | CPU |

CC-BY-4.0 lets you use, change, and share the model, also for commercial
work. You must give attribution to NVIDIA when you distribute the model.
Magi does not include the model files. The engine host downloads them.

The models use different Python environments. Thus each engine has its
own process. `engine/server.py --engine NAME` is the program for all of
them. The service needs the STT and the TTS engine; the other engines are
optional.

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
| `POST /speaker` | speaker | `{"audio" (base64), "sample"? (base64)}` | `{"vector" (base64) or null, "model", "engine", "audio_ms", "elapsed_ms", "extracted"? (base64 WAV), "extracted_vector"?}` |

The engine `detect` is not speech. It is the prompt injection detector of
Magi (ProtectAI deberta v3 v2, Apache-2.0, ONNX on the CPU; pins in
`model-pins.json`). It uses the same host program and token because the
same host runs it. The score of a text is the INJECTION probability of its
worst window of 512 tokens. Magi calls it directly (`MAGI_DETECTOR_URL`);
the speech service does not use it. Environment: Python 3.13 with
onnxruntime 1.31.0 (MIT), tokenizers 0.23.3 (Apache-2.0), and numpy 2.5.4,
in `~/.local/share/magi/speech/installs/detect/venv`; the model is in
`.../detect/models/protectai-v2`.

### Speaker engine

The engine `speaker` makes voice prints and removes other voices from
audio.

- A voice print is the output of the speaker model for a piece of speech:
  192 numbers with unit length (`vector`: little-endian float32). The
  engine drops the quiet start and end of the audio, and it reads at most
  30 seconds. Audio with less than 0.4 seconds of sound has no print
  (`vector` is null).
- With `sample` (speech of one person, 3 seconds or more), the engine
  also runs the extraction: VoxLock keeps the voice of the sample and
  removes the other voices. `extracted` is that audio (WAV, 16 kHz, one
  channel) and `extracted_vector` is its print.
- The extraction can damage the speech. The caller must compare the print
  of the result again and use the better of the two.
- VoxLock ignores a sample that is shorter than 3 seconds. It then keeps
  the loudest voice of the audio, which can be a different person. Thus
  the engine does not run the extraction with a short sample, and the
  service does not take a sample under 3.1 seconds (the margin is for a
  sample that the engine must convert to 16 kHz).
- The engine keeps no audio. It writes the sample to a temporary file only
  while it makes the extraction object, and it keeps that object in memory
  for the last two samples.
- The VoxLock files are optional. Without the directory `voxlock` in the
  model directory, the engine makes prints only.

Measured on a desktop processor with 4 threads: a print of a 3-second clip
takes about 0.1 s. The extraction of 4 seconds of audio takes about 0.8 s,
and of 14 seconds about 2.9 s. The first extraction for a person takes
about 2 s more. The engine uses about 250 MB of memory, and about 280 MB
more for each of the two extraction objects.

The speaker model is from the 3D-Speaker project and VoxLock is from the
VoxLock project. The two repositories have the Apache-2.0 license. The
engine host downloads the files from the sherpa-onnx releases;
`model-pins.json` has their hashes.

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
not in the repository. `install.sh --provision` trains the model on the
engine host, or copies a build (see "Deploy the engines"). Training takes
about 90 s on an RTX 4080 (3 epochs). `train.py` uses a CUDA GPU when torch
sees one, else the CPU, which is much slower.
`model-pins.json` has the hashes of the published build. A build that the
host made itself gets `turn/built.json` with its own hashes and the name
`magi-turn-minilm-l12-v1-local`; the engine checks the files against that.
On 2026-11-05, a build from zero on a CUDA GPU (the same code, seed, and
base model revision) gave the published files, byte for byte. A build on
other hardware can give a slightly different model.
`provision.py` refuses a model that takes more than 2 held-out turns as
acknowledgments.

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
environment `runtime`, the same as `detect` (onnxruntime, tokenizers, numpy).

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
- `get-speaker-profile`, `put-speaker-profile`, `delete-speaker-profile`:
  the voice prints of the caller for one speaker model (below).
- `verify-speaker`: is the speech in the audio the speech of the caller
  (engine `speaker`)? Without `MAGI_SPEECH_SPEAKER_URL`, it returns
  `rpc.Unavailable` for a caller who has a profile. `/healthz` does not
  check this engine.

## Voice prints

A person has one profile for each speaker model. The service knows the
model `3dspeaker-eres2net-en-voxceleb-16k` (192 numbers). A device of the
person makes the prints with the same model: the person reads some lines,
and the device puts the profile. Each device of the person can get it.

- A profile has 1 to 40 prints. `put-speaker-profile` replaces all prints
  of the model. The service gives each print unit length. It refuses a
  print with the wrong count of numbers, with a number that is not
  finite, or with only zeros.
- `source` of a print is `enroll` (the person read a line for it) or
  `turn` (a turn that the device verified).
- `sample` is speech of the person for the extraction: a WAV file with
  PCM audio, 3.1 seconds or more, 2 MB at most. A put with no sample keeps
  the sample that is there.
- `verify-speaker` gives `score`: the cosine of the print of the audio and
  the mean of the prints of the profile. A caller with no profile gets
  `enrolled` false and the score 0, and no engine runs. With `extract`
  and a sample in the profile, the response also has `extracted` (the
  audio with only the voice of the person) and `extracted_score`.
- The service does not decide if a score is enough. The caller has the
  limit (Magi: `magi/compose/voiceprint.go`).
- A profile is not a credential. It selects speech. A voice must never
  authorize an action.

Measured on 25 November 2026 with speech of one person from a phone and
26 other speakers (quiet audio): 1.5 seconds of the person scored 0.36 or
more, and the other speakers scored 0.34 or less. With other voices in the
audio, the score of the person falls; the extraction brings a part of it
back.

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
| `-speaker-url` | `MAGI_SPEECH_SPEAKER_URL` | optional, such as `http://speech.example.local:8096` |
| `-database-url` | `MAGI_DATABASE_URL` | optional: PostgreSQL for the lexicon and the voice prints |
| `-engine-token-file` | `MAGI_SPEECH_ENGINE_TOKEN_FILE` | required |

The service reads the engine token one time, at start.

## Deploy the engines

Run this from a checkout. The engine host must be Linux with `ffmpeg`,
`curl`, and SSH access. It needs no root.

```sh
primitives/speech/engine/install.sh --provision user@speech.example.local
```

`--provision` runs `engine/provision.py` on the host first:

1. It installs a pinned `uv` in `~/magi-speech/bin` if the host has no `uv`.
   `uv` gets the Python versions (3.12 for TTS and training, 3.13 for the
   other engines).
2. It makes the environments in `~/.local/share/magi/speech/envs/` from the
   lock files in `engine/requirements/` (pinned versions, with hashes). An
   environment whose lock did not change is not made again.
3. It downloads the models to `~/.local/share/magi/speech/models/` and checks
   each file against `model-pins.json`: Parakeet (from the sherpa-onnx
   release), Kokoro and the injection detector (from Hugging Face at the
   pinned revision), Silero VAD (from GitHub at the pinned tag), and the
   speaker model and VoxLock (from the sherpa-onnx releases).
4. It trains the turn classifier (above) in the environment `train`. To
   copy a build from another host in its place, give the directory:
   `MAGI_SPEECH_PROVISION_ARGS="--turn-from DIR"`.
5. It writes the paths to `~/.local/share/magi/speech/engines.env`.

Keep `MAGI_SPEECH_ROOT` short (the default is good). espeak-ng, which Kokoro
uses, cuts a data path of 160 bytes or more, and then the TTS engine does
not start; `provision.py` refuses such a root.

The TTS torch build comes from the host: CUDA when `nvidia-smi` answers,
ROCm when the host has `/dev/kfd` and `rocminfo`, else the CPU. To choose,
use `MAGI_SPEECH_PROVISION_ARGS="--torch cpu"` (or `cuda`, `rocm`). The CUDA
build of torch is about 3 GB and the ROCm build about 16 GB. The lock files
are for x86_64. To change a package, edit the `*.in` file and run
`engine/requirements/compile.sh`.

Then `install.sh` does these steps (also without `--provision`):

1. It copies `server.py`, `model-pins.json`, and `supervise.sh` to
   `~/magi-speech/` on the host.
2. It finds the environments and models: an explicit value first, then
   after `--provision` the paths of `engines.env`, then the values of the
   last install, then `engines.env`, then the older layout under
   `~/.local/share/magi/speech/installs/*/`. To use other paths, set
   `MAGI_SPEECH_{STT,TTS,DETECT,TURN,SPEAKER}_{PYTHON,MODEL}`. Set ports
   and threads with `MAGI_SPEECH_{STT,TTS,DETECT,TURN,SPEAKER}_{PORT,THREADS}`.
3. It makes `~/magi-speech/token` (mode 600) if the file does not exist. It
   does not show the token.
4. It starts or restarts the engines and waits for `/health`.

If user lingering is on (`loginctl enable-linger USER`, as root), systemd
user units `magi-speech-{stt,tts,detect,turn,speaker}` run the engines. If lingering
is off, a `nohup` supervisor loop runs each engine and restarts it after a
crash. The loop does not continue after a host restart. Then run
`install.sh` again. Logs and PID files are in `~/magi-speech/`.

Put the token in the Secret `magi-speech-engine` without showing it:

```sh
ssh user@speech.example.local cat magi-speech/token | \
  kubectl -n magi create secret generic magi-speech-engine --from-file=token=/dev/stdin
```

To add one engine to a host that has the others, name it. The paths of
the other engines stay:

```sh
MAGI_SPEECH_PROVISION_ARGS="--engines speaker" \
  primitives/speech/engine/install.sh --provision user@speech.example.local
```

Then set `SPEECH_SPEAKER_URL` (`deploy/README.md`) and deploy the speech
service.

### TTS on a GPU

The TTS engine uses a GPU when its Python environment has a CUDA or ROCm
build of PyTorch (BSD-3-Clause; ROCm is MIT and similar licenses).
`--provision` makes that environment when the host has the GPU (see above).
For a host with more than one GPU, give the engine environment:

```sh
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

## Tests

Unit tests use a fake engine, the real rpc HTTP transport, and the generated
client:

```sh
cd primitives/speech && go vet ./... && go test ./...
```

The lexicon test and the voice print test need PostgreSQL. Each makes a
database, owned by a new role, and drops both at the end. They skip when
`MAGI_TEST_DATABASE_URL` is not set:

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

The test of the speaker engine uses the real models. It needs the Python
of the engine environment, the model directory, and the test files of the
VoxLock model package (`en-voice-sample-f.wav`, `en-combined.wav`). It
skips without them:

```sh
MAGI_SPEAKER_TEST_MODEL_DIR=~/.local/share/magi/speech/models/speaker \
MAGI_SPEAKER_TEST_WAVS=/path/to/sherpa-onnx-voxlock-int8/test_wavs \
~/.local/share/magi/speech/envs/speaker/bin/python -I primitives/speech/engine/speaker_test.py
```

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
- Voice prints: one speaker model, trained on English speech. The numbers
  above are from one person and quiet audio. A print from 1 to 2 seconds
  of speech in a noisy room has more errors.
- VoxLock is a new project (October 2026). Its extraction runs only for a
  turn that the device was not sure about.
- Parakeet does not give a confidence value. `confidence` is not set.
- The engine token is a shared secret. Engine traffic uses plain HTTP. Use
  the engines only on a trusted network.
- Parakeet sometimes writes "Magi" as "Magei". Kokoro sometimes says "4-6" as
  "four six".
