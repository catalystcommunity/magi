# TTS candidate audition

This experiment compares stock voices with fixed English text. It uses Kokoro
82M and Qwen3-TTS 1.7B CustomVoice. Each engine has its own adapter. The same
runner records audio, timing, configuration, and model file hashes.
See the [measured results](../../../docs/tts-candidate-test.md) for the host test.

The experiment does not deploy a TTS service. It does not record a microphone,
clone a personal voice, change a driver, or use private context. Pocket TTS is
excluded at the user's request. Chatterbox needs a separate permitted stock
voice catalogue before its audition.

## Listen to the samples

From the repository root, run:

```sh
npm run dev:tts:listen
```

Open `http://127.0.0.1:8093/`. The server listens on loopback only. It serves
the listening page, its catalogue, and WAV files from the results directory.
It does not serve the repository or model directory.

Use `npm run dev:tts:listen -- --port 8094` if the default port is in use.
Use `--results /path/to/results` for another audition.

Select a test text. Compare the voices at a safe volume. Record pronunciation
errors and voices that you want to keep. Ratings and notes stay in browser
storage. Use **Export my review** to save a JSON file. Hidden labels reduce
name-based preference, but they do not make this a randomized blind trial.

Generated WAV files are excluded from Git. Keep them with their manifests for
listening. The runner can regenerate them from the pinned files, but hardware
and library changes can change the audio. File hashes identify each tested output.

## Prepare a CPU environment

Use Linux x86-64 and Python 3.12.12 for the recorded configuration. The published
Kokoro 0.9.4 package excludes Python 3.13. Do not change system Python to meet
this requirement. Use a separate environment.

The resolved packages and archive hashes are in `requirements.lock`. Model
revisions are in `audition.py`. The lock includes the CPU builds of PyTorch and
TorchAudio. Use the explicit CPU index during installation.

The following commands use an example experiment path. Select a new directory
for each installation. Do not use a system service directory.

```sh
uv venv --python 3.12.12 --seed /tmp/opal-tts-example/venv
uv pip sync \
  --python /tmp/opal-tts-example/venv/bin/python \
  --require-hashes \
  --extra-index-url https://download.pytorch.org/whl/cpu \
  --index-strategy unsafe-best-match \
  experiments/speech/tts/requirements.lock

HF_HOME=/tmp/opal-tts-example/hf-cache \
HF_HUB_DISABLE_IMPLICIT_TOKEN=1 HF_HUB_DISABLE_TELEMETRY=1 \
  /tmp/opal-tts-example/venv/bin/python experiments/speech/tts/audition.py prepare \
  --models /tmp/opal-tts-example/models
```

Preparation downloads the pinned public artifacts without an account token.
It records a SHA-256 hash for each selected model file and voice profile.
These hashes identify the downloaded bytes. They are not publisher signatures.
Keep the model cards and license attribution with any later deployment assets.

`requirements.in` is the input for a new lock. Do not resolve it again to repeat
the recorded test. A new resolution can select different dependencies.

## Generate samples

From the repository root, run the following commands in sequence. Do not run
another inference benchmark on the same host at the same time.

```sh
/tmp/opal-tts-example/venv/bin/python experiments/speech/tts/audition.py run \
  --engine kokoro --models /tmp/opal-tts-example/models \
  --output /tmp/opal-tts-example/results/kokoro --threads 4

/tmp/opal-tts-example/venv/bin/python experiments/speech/tts/audition.py run \
  --engine qwen --models /tmp/opal-tts-example/models \
  --output /tmp/opal-tts-example/results/qwen --threads 4 \
  --prompt-ids ack introduction
```

Use `--voices` to select stock voice IDs. Use `--prompt-ids` to select prompts.
Use `--repeats` for one to five measured repetitions. The default seed is 1729.
Each repetition increases the seed by one. A separate warmup waveform and
measurement precede the test samples.

The output directory must not exist. This prevents an accidental overwrite of
earlier evidence. The runner saves a manifest after each sample. A controlled
interruption records incomplete status. A hard kill can leave running status;
the verifier rejects it. Apply host resource limits and an external timeout
when you run these models on a shared host.

Synthesis uses local files with Hugging Face offline mode. Kokoro's English
language data is installed with the locked packages. The test needs no model
download during inference.

Upstream libraries also write messages to stdout and stderr. Read results from
the manifest, not from a presumed JSON-only output stream. Qwen reports missing
SoX and FlashAttention on this CPU setup. The tested CustomVoice calls complete
without those optional components. Do not change host packages to hide warnings.

## Interpret the measurements

The real-time factor is generation time divided by audio duration. A value below
one means that generation is faster than playback. First-audio time starts at
the adapter call and ends at its first waveform yield. It excludes transport,
playback, and the model load.

Kokoro yields phoneme-bounded text chunks. These fixtures fit within one chunk.
Qwen's selected Python API returns a complete utterance. Its
`non_streaming_mode` option does not enable incremental waveform output.
Do not label either measurement as end-to-end streaming latency.

The runner uses CPU float32 and four inference threads. It records the model
construction time, package versions, output amplitude, PCM clipping count, and
Linux peak process resident memory. It does not clear filesystem caches.
It does not normalize output volume. Compare playback levels carefully.

## Check the evidence

```sh
/tmp/opal-tts-example/venv/bin/python -m unittest discover \
  -s experiments/speech/tts -p 'test_*.py' -v

node experiments/speech/tts/listen.test.mjs

python3 experiments/speech/tts/verify.py \
  --results /tmp/opal-tts-example/results
```

The verifier checks sample completeness, hashes, WAV format, duration, and
timing consistency. It writes `summary.json` from the retained measurements.

The process interruption probe uses a new output directory:

```sh
/tmp/opal-tts-example/venv/bin/python experiments/speech/tts/cancel_probe.py \
  --engine kokoro --models /tmp/opal-tts-example/models \
  --output /tmp/opal-tts-example/checks/cancel-kokoro
```

It sends SIGTERM after warmup, during the next request. It checks the process
exit and retained interruption status. This does not test a service cancellation
API or suppression of audio already queued on a client.

An optional Parakeet check transcribes each generated sample. Use the separate
Sherpa-ONNX environment from the [Parakeet test](../../../docs/parakeet-cpu-test.md):

```sh
python experiments/speech/tts/transcribe.py \
  --model-dir /path/to/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8 \
  --results /tmp/opal-tts-example/results
```

This writes `transcripts.json`. The listening page shows each transcript in a
closed detail section. A disagreement can be a synthesis error or a recognition
error. One recognizer cannot establish naturalness, speaker identity, or exact
pronunciation. No word-error-rate claim is made.

## Next Actions

The [user review](../../../docs/tts-candidate-test.md#user-review) kept twelve
Kokoro and seven Qwen candidates from the introduction samples. Check the two
remaining listening flags. Select persona and fallback mappings. Test the
selected backend through the speech contract with cancellation,
concurrent requests, and permitted fallback. Keep host-specific performance
results separate from voice quality and from GPU results.
