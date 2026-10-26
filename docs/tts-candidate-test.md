# TTS candidate test

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. See the
[configuration overlay](../config/README.md).

Test date: 2026-10-17. Host: the private speech host, `192.0.2.20`.

Kokoro is a practical first CPU candidate for Magi. Qwen3-TTS 1.7B CustomVoice
also runs on this host, but the tested CPU path is too slow for live replies.
The user kept twelve Kokoro voices and seven Qwen voices after the introduction
comparison. These results do not compare either engine's quality with Breeze.

## Listen and select voices

Run `npm run dev:tts:listen` from the repository root. Open
`http://127.0.0.1:8093/`. Select a test text, compare voices, and export your
review. Use `-- --port 8094` if port 8093 is in use.

The page has twelve Kokoro voices and nine Qwen voices. It can hide their names and timing
data. Ratings and notes stay in browser storage. No personal voice was recorded
or cloned. All test text is fictional. No persona voice has been selected yet.

## User review

The user supplied a [review export](evidence/experiments.md#experiment-030)
dated 2026-10-17. All 21 ratings refer to the common introduction.

| Engine | Keep | Reject |
| --- | --- | --- |
| Kokoro | `af_heart`, `af_bella`, `af_nicole`, `af_sarah`, `af_sky`, `am_fenrir`, `am_michael`, `am_puck`, `bf_emma`, `bf_isabella`, `bm_george`, `bm_fable` | None |
| Qwen | `ryan`, `sohee`, `serena`, `vivian`, `dylan`, `eric`, `ono_anna` | `aiden`, `uncle_fu` |

The [review summary](evidence/experiments.md#experiment-031)
records the choices and hashes of the source export, archived copy, and manifests.
Each reviewed clip matched its manifest and audio hash. The archived export adds
one final newline; its JSON content is unchanged.

The twelve accepted Kokoro candidates exceed the eight-voice target for the
first CPU service test. These ratings do not rank the engines or assign voices
to personas. They do not close the number and long-prompt checks below.
Select permitted fallback mappings separately.

## Test configuration

- Kokoro 82M, package 0.9.4, with twelve stock English voices.
- Qwen3-TTS 1.7B CustomVoice, package 0.1.1, with nine stock speakers.
- Python 3.12.12, PyTorch 2.9.1 CPU, and float32 inference.
- Four inference threads and one inter-operation thread.
- A separate warmup, then serial requests with seed 1729.
- Mono 24 kHz output, saved as 16-bit PCM without volume normalization.

The [runner and procedure](../experiments/speech/tts/README.md) contain the
repeat commands. The dependency lock includes archive hashes. The manifests
contain exact model revisions, file hashes, settings, and measurements.
Model hashes are checked before synthesis. Inference uses local model files.

The model cards declare Apache-2.0 for
[Kokoro](https://huggingface.co/hexgrad/Kokoro-82M) and
[Qwen CustomVoice](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice).
Keep model attribution and dependency notices with a later deployment.

## Initial CPU measurements

| Measurement | Kokoro | Qwen CustomVoice |
| --- | --- | --- |
| Short reply, first audio | 0.19 to 0.24 seconds | 8.75 to 12.98 seconds |
| Common introduction, first audio | 0.97 to 1.99 seconds | 46.40 to 60.94 seconds |
| Short reply, median real-time factor | 0.105 | 4.957 |
| Common introduction, median real-time factor | 0.119 | 4.914 |
| Model construction in the measured process | 6.86 seconds | 5.45 seconds |
| Peak process resident memory in the voice comparison | 3.30 GiB | 11.13 GiB |

The real-time factor is generation time divided by audio duration. Values below
one mean that generation is faster than playback. These are observed ranges,
not latency percentiles. The test does not measure network or playback delay.
The [summary](evidence/experiments.md#experiment-032) links
each run to its manifest hash. It is calculated from the retained samples.

Kokoro yields text-sized chunks. The test prompts fit within one chunk.
Qwen's selected Python API returns a complete waveform. Its
`non_streaming_mode` option does not provide incremental waveform delivery.
The [upstream implementation](https://github.com/QwenLM/Qwen3-TTS/blob/main/qwen_tts/inference/qwen3_tts_model.py)
states this limit. Neither first-audio column is a network streaming benchmark.

The first Kokoro run overlapped Qwen model startup during its last long samples.
Its short replies and common introductions completed before that overlap.
Do not use the last long samples as isolated-load performance measurements.
A separate serial repeat used `af_heart` and `am_fenrir` with three seeds each.
Its long samples took 2.52 to 2.97 seconds. All six seed-1729 output hashes
matched the corresponding first-run files. See the
[repeat summary](evidence/experiments.md#experiment-029).
Model construction includes local initialization but not package installation.
Filesystem caches were not cleared. The host was not reserved for this test.

## Interpretation limits

The listening set contains 81 clips. Kokoro covers all five prompts in twelve
voices. Qwen covers the short reply and introduction in nine voices. Its Ryan
voice also covers the other three prompts. The Ryan long sample contains 32.32
seconds of audio and took 148.69 seconds to generate.

The listening set tests short replies, questions, names, technical terms, dates,
amounts, negation, and a longer explanation. The numeric prompt spells out its
numbers. It does not test conversion from raw dates, currency symbols, or digits.

Output amplitude and successful WAV decoding do not establish naturalness or
intelligibility. A local Parakeet transcript can identify samples for review.
It can also contain recognition errors. Do not use it as a human quality score.

Qwen stock speakers have different native languages. English output must be
reviewed for each speaker. The CPU result does not establish GPU latency.
No CUDA or AMD acceleration path was tested.
Style instructions, sentence splitting, and concurrent synthesis were not tested.

Pocket is excluded at the user's request. Its current model access requires
account approval. Chatterbox remains outside this first comparison because it
needs a permitted stock reference-voice catalogue. Personal voice enrollment
is not a way to close that gap.

## Validation and review flags

Nine harness tests passed. They cover invalid audio, prompt validation,
failed-run evidence, missing or changed samples, and HTTP path restrictions.
Three JavaScript tests passed for review storage and export across tabs.
The verifier checked all 81 audition clips and 18 serial-repeat clips.
No measured sample exceeded the PCM amplitude limits.

Both process interruption probes passed. The process exited about 0.665 seconds
after SIGTERM and saved interrupted status without a completed test sample.
See the [Kokoro result](evidence/experiments.md#experiment-027)
and [Qwen result](evidence/experiments.md#experiment-028).
These results do not prove API cancellation or suppression of queued audio.

Browser checks passed for filters, hidden labels, WAV decoding, playback,
single-player operation, local review storage, and JSON export.

The [local Parakeet check](evidence/experiments.md#experiment-033)
retains 81 transcripts. All thirteen number-prompt transcripts retained
"Do not send the payment." Two samples need a specific listening check:

- Kokoro `am_puck`, numbers: Parakeet omitted part of "forty two".
- Qwen `ryan`, long explanation: Parakeet returned "the sharp" for "the shared service".

These are transcript disagreements, not confirmed synthesis errors. Names such
as Janaya and LinkKeys also have different spellings in the transcripts.
Listen to the source audio before accepting a correction or rejecting a voice.

## Host changes

The isolated environment is `/tmp/opal-tts-20261017.NpLmvn` on the speech host.
It contains its own Python runtime, packages, models, and generated samples.
The temporary files use about 6.6 GiB. They can be reused for the next test.
No system package, driver, speech service, router entry, or Kubernetes resource
changed. The local listening server binds only to `127.0.0.1`.

The experiment is not a deployed TTS API. It does not implement LinkKeys access,
request admission, playback state, voice fallback, or service-level cancellation.
The existing local Q&A and Example environment deployments were not changed.

## Next Actions

1. Use the twelve accepted Kokoro candidates for the first CPU speech service test.
2. Check the flagged number and long samples. Select persona voices and permitted
   fallback mappings before their use in the connected demo.
3. Keep Qwen as an optional candidate for an admitted accelerated host or runtime.
4. Test request deadlines, concurrent load, cancellation, and permitted fallback
   through the speech contract before a connected voice demo.
