# Voice evals

These scripts measure the models of the voice front end of the Android
app (`android/README.md`, "Voice"): the voice print, the spotter of the
wake names, and the extraction of one voice from noise. The limits in
`android/.../VoicePrints.kt`, `android/.../Senses.kt`, and
`magi/compose/voiceprint.go` come from these measurements.

The audio is not in the repository. It is speech of real people.

## Data

Make a work directory with:

| Path | Contents |
| --- | --- |
| `utts.txt` | One line for each clip: `id|author_kind|modality|author|digest|created_ms|text`. Export it from the conversation service of an installation (the voice messages of a person who agrees, and the spoken messages of the personas). |
| `a16/<author_kind>-<modality>-<id>.wav` | The clips, 16 kHz, one channel |
| `libri/` | LibriSpeech `dev-clean-2` (other speakers) |
| `models/` | The models from the releases of sherpa-onnx: the speaker models, `sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01`, `sherpa-onnx-voxlock`, `gtcrn_simple.onnx`, `dpdfnet2.onnx` |

Install `sherpa-onnx`, `numpy`, `soundfile`, `sentencepiece`, `pypinyin`,
and `protobuf` in a Python environment. Set `VOICE_EVAL_OWNER` to a part
of the subject of the person whose voice is the owner.

## Scripts

| Script | Measures |
| --- | --- |
| `eval_spk.py [seconds]` | Each speaker model: the scores of the owner (leave one out), of other speakers, and of the persona voices; the equal error rate; and the scores of clips that are the echo of a persona |
| `eval_kws.py MODEL_DIR boost threshold` | The spotter: names found, and false wakes in the other speech. `KW` has the names, with commas. |
| `eval_noise.py gtcrn,dpdfnet,sherpa-onnx-voxlock` | The score of the owner in babble and with one other speaker, with no processing, after a denoiser, and after the extraction |

## Results of 25 November 2026

The set: 22 turns of one person from a phone and a browser, 26 other
speakers (156 clips), 265 spoken messages of three persona voices. The
room was quiet. The set is small: the numbers show the order of the
models and the place of the limits, not the error rate of a household.

### Voice print

The score is the cosine of a print and the mean print of the person.

| Model | Size | Errors, whole turns | Errors, first 1.5 s | Time for a turn (desktop, 2 threads) |
| --- | --- | --- | --- | --- |
| 3D-Speaker ERes2Net, English | 26 MB | 0.2% | 0.0% | 86 ms |
| TitaNet small | 40 MB | 0.1% | 0.8% | 33 ms |
| TitaNet large | 101 MB | 0.0% | 0.8% | 91 ms |
| 3D-Speaker CAM++, English | 30 MB | 3.7% | 1.1% | 33 ms |
| NeMo SpeakerNet | 23 MB | 0.5% | 4.6% | 21 ms |
| WeSpeaker models (CAM++, ResNet34, ResNet221) | 27 to 95 MB | 18% to 26% | 18% to 32% | |

- ERes2Net on 1.5 s: the person scored 0.36 or more, and the other
  speakers 0.34 or less (0.42 or less on whole turns).
- Five clips that Magi had kept as turns of the person were the echo of a
  persona. ERes2Net gave them 0.01 to 0.24 against the person and 0.32 to
  0.65 against a persona voice.

### Wake names

The Zipformer spotter (GigaSpeech, 3.3 million parameters), names
"Archimedes" and "Beatrice", boost 2.0, threshold 0.15:

| Speech | Result |
| --- | --- |
| Names from the phone of one person | 6 of 6 |
| Names in a synthetic voice | 25 of 28 |
| 122 minutes of read speech of other people | 1 false wake |
| 92 minutes of the persona voices | 0 false wakes |

The archive `...-mobile` of the model has an encoder that fails in
sherpa-onnx 2.0.3. Use the archive with no `-mobile`.

### Noise

Seven turns of the person, mixed with LibriSpeech speakers. The numbers
are the median score of the person; "other" is the highest score of the
same noise with no person in it.

| Noise | No processing | After the extraction (VoxLock) | Other |
| --- | --- | --- | --- |
| Babble of six, 15 dB | 0.66 | 0.67 | 0.28 |
| Babble of six, 10 dB | 0.57 | 0.62 | 0.22 |
| Babble of six, 5 dB | 0.43 | 0.48 | 0.22 |
| Babble of six, 0 dB | 0.18 | 0.39 | 0.23 |
| One other speaker, 10 dB | 0.44 | 0.65 | 0.14 |
| One other speaker, 5 dB | 0.40 | 0.63 | 0.14 |
| One other speaker, 0 dB | 0.33 | 0.15 | 0.25 |

- The denoisers (GTCRN, DPDFNet) did not help the voice print.
- The extraction helps most with one other speaker. It made some clips
  worse (the lowest score after it was near 0), so Magi keeps the better
  of the two scores.
- The extraction ran at 0.27 of real time on four desktop threads.

## Not measured

- A real phone in a real noisy place, far speech, and the echo of the
  loudspeaker of the phone.
- People with similar voices (a family).
- The battery use of the phone.
