# Parakeet CPU smoke test

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. See the
[configuration overlay](../config/README.md).

Tested 2026-10-16 on the private speech host, `192.0.2.20`. The offline Parakeet path works
on this host without CUDA. This is one public-fixture smoke test, not a speech
service deployment or a household accuracy evaluation.

## Configuration

- Model: `sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8`.
- Runtime: `sherpa-onnx==1.13.8`, `sherpa-onnx-core==1.13.8`.
- Python: 3.13.5. NumPy: 2.3.4.
- CPU execution: two inference threads; greedy decoding.
- Fixture: upstream `test_wavs/en.wav`, 3.845 seconds, mono 24 kHz PCM.
- Features: 16 kHz. Sherpa resampled the input.
- Process limit: 8 GiB virtual memory and a 180-second timeout.
- Model source: the [official Sherpa archive](https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2).

The [upstream instructions](https://k2-fsa.github.io/sherpa/onnx/pretrained_models/offline-transducer/nemo-transducer-models.html)
describe the model path. [NVIDIA's model card](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)
states the source model's CC-BY-4.0 terms. Keep attribution with deployment assets.

## Results

| Measurement | Result |
| --- | --- |
| Model construction | 0.813 seconds |
| First decode in measured process | 0.126 seconds |
| Second decode | 0.125 seconds |
| Third decode | 0.123 seconds |
| Decode time divided by audio duration | 0.0319 to 0.0328 |
| Peak process resident memory | 883,916 KiB, about 863 MiB |
| Output consistency | Identical transcript in all three measured runs |

The transcript matches the fixture's recognizable Kennedy quotation. No formal
word-error-rate evaluation was performed. The
[retained result](evidence/experiments.md#experiment-026)
contains exact timings, output, and observed model and audio hashes. These hashes
identify the tested bytes; they are not an independent upstream signature check.

An earlier exploratory run used a 24 kHz feature setting. The recorded result
uses the corrected 16 kHz setting. Filesystem caches were not cleared, so model
construction is not a cold-host startup measurement. Three runs of one short
sample do not establish p95 latency, concurrent capacity, or recognition quality.

## Repeat the measurement

Use the [benchmark script](../scripts/benchmark-parakeet.py) with the pinned
runtime and extracted model. The script installs nothing and makes no network
requests. It accepts an explicit public mono 16-bit PCM WAV of at most 120 seconds.

```sh
python scripts/benchmark-parakeet.py \
  --model-dir /path/to/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8 \
  --wav /path/to/public-fixture.wav \
  --threads 2 --runs 3
```

The temporary environment and downloaded model remain at
`/tmp/opal-parakeet-20261016.gj22Yg` on the host. No service, driver, router
setting, or system Python package changed. No microphone or private recording
was used. The experiment's existing service was not started or stopped.

## Next Actions

Test longer speech, silence, noise, names, numbers, negation, and cross-talk.
Measure concurrent requests and full capture-to-result latency. Keep speaker
attribution and multi-engine transcript agreement as separate evaluations.
