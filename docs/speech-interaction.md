# Speech interaction: research and plan

Research date: 2026-10-31. This document is for people who design or review
Magi. It tells the state of the art for spoken interaction with a household
assistant, what Magi can use, and in which order to build it. We did not
test new models for this document. The Magi numbers come from earlier tests
on lab-nas (`primitives/speech`).

Today Magi has speech to text (STT) with Parakeet TDT 0.6B (sherpa-onnx, CPU)
and text to speech (TTS) with Kokoro 82M (CPU). Neither streams. Neither
knows who speaks.

## 1. Decisions

1. **Keep a cascade** (speech to text, text model, text to speech). Do not
   use a full-duplex speech-to-speech model now. Open full-duplex models
   (Moshi, PersonaPlex 7B) need most of the 16 GB GPU. They also have no
   text step where Magi can check identity, permissions, and tools.
2. **The largest latency gain is TTS on the first clause, plus a turn
   detector.** Streaming STT is second.
3. **Voice is not authentication.** It selects the speaker, the persona,
   and personal content. The identity comes from the device: each voice
   call carries the LinkKeys attestation of an enrolled device. Sensitive
   actions also need a second factor that lasts for a time on that device.
4. **Overlapping speech on one phone microphone is not solved.** Find it,
   mark it, and ask people to repeat.
5. **A second review of a transcript helps only on risky parts**: numbers,
   names, dates, money, and commands. An LLM must select among candidates.
   It must not write the transcript again.
6. **"4-6" and "README" are text normalization errors.** Rules and a
   household lexicon fix them. No new model is necessary. This is done
   (section 9.5).
7. **Magi does not ship images for speech or models.** Magi gives tools
   that set up a host as a model or speech host. The tools download the
   models (for example from Hugging Face) to the host.

## 2. Streaming

### 2.1 Speech to text

| Model | Licence | Streaming | Measured |
| --- | --- | --- | --- |
| Parakeet TDT 0.6B v3 (now) | CC-BY-4.0 | No | 155 ms for 5 s of audio on lab-nas CPU |
| nemotron-speech-streaming-en-0.6b | NVIDIA Open Model License (custom) | Yes: 80, 160, 560, 1120 ms chunks | WER 7.84% at 160 ms (NVIDIA). int8 builds for sherpa-onnx and Android exist. No published CPU speed. |
| Nemotron-Labs Unified ASR 0.6B (2026-10-27) | OpenMDW-1.1 | Yes, and offline in the same model | English WER 4.68% offline, 5.41% at 1.12 s (NVIDIA). GPU (NeMo) only now. |
| Kyutai STT 1B | CC-BY-4.0 | Yes, 0.5 s | GPU |
| Moonshine v2 (34M to 245M) | MIT | Yes | WER 6.65–12.0%. Small, for CPU. Can repeat words on hard audio. |

When a turn detector finds the end of speech quickly, Parakeet on the last
segment is already fast. Streaming STT helps with long utterances, with an
early start of the LLM, and with barge-in that needs words.

### 2.2 Text to speech

Kokoro does not accept text while the LLM writes it. Magi gets the same
result when it divides the LLM output at the end of each clause and sends
each part to Kokoro. Make the first part short (one clause). Make the next
parts full sentences. Very small parts make the intonation bad.

| Model | Licence | Note |
| --- | --- | --- |
| Kokoro 82M (now) | Apache-2.0 | 0.19–0.24 s for short replies on lab-nas CPU |
| Qwen3-TTS 0.6B and 1.7B | Apache-2.0 | Accepts streaming text, first audio about 100 ms on GPU (vendor). Takes style instructions. Too slow on CPU (RTF 4.9 for 1.7B on lab-nas). |
| Kyutai TTS 1.6B | CC-BY-4.0 | Accepts streaming text, about 220 ms (vendor), GPU |
| Chatterbox Turbo and Nano | MIT | Tags such as `[laugh]`. A watermark is in all audio. |
| Orpheus 3B | Apache-2.0 | Tags such as `<sigh>`. GPU. |
| Piper | **GPL-3.0** since v1.7.0 | Do not link it into Magi code. |
| Fish S2, OpenAudio S1 | **Non-commercial** | Do not use. |

### 2.3 Voice activity, end of turn, barge-in

- **Voice activity detection:** Silero VAD v6 (MIT, about 2 MB).
- **End of turn:** Pipecat Smart Turn v3 (BSD-2, 8 MB). It reads audio and
  tells if the person has finished. It takes 10–65 ms on CPU. Run it when
  VAD finds a pause of 200 ms. Do not use the LiveKit turn detector: its
  weights have a custom licence.
- **Echo cancellation on Android:** record with the `VOICE_COMMUNICATION`
  source. The device then applies its echo canceller. Quality changes with
  the device.
- **Satellites (for example a Raspberry Pi):** use a microphone board with
  echo cancellation and beamforming, for example XMOS XVF3800.
- **Barge-in rule:** do not stop playback on VAD alone. Stop when VAD fires
  and STT gives one or two words that are not the echo of the text that
  Magi plays now. Record the words that Magi actually played before the
  stop.

### 2.3.1 Interruptions while the persona speaks (design, 2026-11-04)

Magi must stop quickly when the person takes the turn, and must not stop for
echo, "mm-hm", or other people in the room. After the stop, Magi records
where the person interrupted and why, for the conversation graph and the
knowledge analysis.

**Status (2026-11-04):** items 1 and 2 are done in a first form: spoken
replies come in parts with word times (`listen`), the web client marks the
word that the person hears, and a client reports the delivery
(`report-delivery`, with `spoken_until`). Typing or the microphone button
stops the speech now. Step 3 is done: the persona turn records
`interrupts`, `reads-ahead`, or `cuts-off` with the target span
(`docs/conversations.md`), and the persona sees only what the person
heard. Not done: microphone frames in the session, VAD on the device, and
the step 2 decision (backchannel and echo).

**What is necessary first:**

1. A voice session in the API. The client sends microphone frames and its
   playback position. Magi sends the reply as audio parts (one for each
   clause) with word times. Today `say` gives one whole clip.
2. Word times. Kokoro's `KPipeline` gives a start and end time for each
   English token (from the predicted durations). The speech service must
   return them with each audio part. The client then knows the "spoken
   cursor": the character offset of the word that the person hears now.
3. On the device: echo cancellation (Android `VOICE_COMMUNICATION`; in the
   browser, `getUserMedia` with `echoCancellation`) and Silero VAD on
   frames of 32 ms.
4. Words early: streaming STT (phase 2), or Parakeet on a rolling buffer
   every 300 ms while the persona speaks.

**Step 1, at once (on the device, less than 100 ms):** when VAD finds speech
during playback, lower the volume to about 25 %. Do not stop. Record the
start time and the spoken cursor at that time.

**Step 2, the decision (300–700 ms after the start of speech):**

| The first words are | Then |
| --- | --- |
| The words that Magi plays near the cursor | Echo. Set the volume again. |
| A backchannel ("mm-hm", "yeah", "right", a laugh), shorter than 600 ms | Continue. Record a backchannel at the cursor. |
| From a voice that is not the person of this stream (speaker ID), or not to Magi | Continue. Record an overlap. |
| Other words, or speech longer than 700 ms with no words yet | The person takes the turn. Stop playback, and send an interrupt frame that stops the LLM and TTS of this reply. |

**Step 3, after the stop (not on the fast path): read ahead or
interrupt.** The librarian gets the reply text, the spoken cursor, the
time when the client showed the text, and what the person said. It finds
the sentence of the reply that the person answers (embeddings first, then
the model):

| The sentence that the person answers | Kind | Anchor |
| --- | --- | --- |
| Ends before the cursor | `interrupts`: a reaction to what the person heard | The sentence; the trigger is near the cursor minus about 300 ms (reaction time) |
| Is after the cursor, and the text was on the screen long enough to read it (about 4 words a second) | `reads-ahead`: the person read it | The sentence |
| No sentence of the reply | `cuts-off`: a new topic, or the person does not want the rest | The cursor |

When the text was not on the screen, `reads-ahead` is not possible.

**What Magi records:**

- On the persona message: the delivery. `spoken_until` (character offset),
  `shown_ms`, `stopped_ms`, and the reason for the stop.
- A link from the person's message to the persona message, with the kind
  from step 3 and the anchor as a character range in the persona message.
- Backchannels and overlaps: marks on the persona message at the cursor.
  They are not messages.
- For the knowledge analysis: text after `spoken_until` that the person did
  not read did not reach the person. Magi must not think that the person
  knows it. The persona can say it later if it is important.

### 2.4 Latency

Measured reference (Daily, 2026, one RTX 5090, server side, median): STT
19 ms, LLM first byte 171 ms, TTS first byte 108 ms, voice to voice 508 ms.
The client adds about 250 ms.

Magi target on the local network:

| Step | Target (median) |
| --- | --- |
| End of speech to end-of-turn decision | 250 ms |
| Last STT segment (Parakeet on lab-nas) | 100–200 ms |
| Network, phone to server and back | 30–60 ms |
| LLM to the first clause (9B on the 4080) | 150–300 ms |
| Kokoro, first clause | 150–250 ms |
| Playback buffer | 50 ms |
| **Total** | **750–1,100 ms** |

### 2.5 What to copy from Pipecat and LiveKit

Magi does not use these frameworks as a runtime. Copy these patterns:

1. Stages that pass typed frames. An interrupt frame stops all later stages
   at once.
2. Time to first byte for each stage, in each turn.
3. A sentence aggregator between the LLM and TTS.
4. The turn detector on CPU, so the GPU stays free.
5. When the LLM and TTS share a GPU, alternate small parts of their work.

## 3. Who is speaking

### 3.1 Diarization (which parts are the same speaker)

| Model | Licence | Streaming | Measured (vendor) |
| --- | --- | --- | --- |
| Nemotron-3-Diarization (2026-09-23) | OpenMDW-1.1 | Yes, 0.32–1.04 s | Maximum 8 speakers, 100M parameters. DER DIHARD3 13.18% at 1.04 s. GPU. |
| Streaming Sortformer 4spk v2.1 | NVIDIA Open Model License | Yes | Maximum 4 speakers. DER DIHARD3 20.21%. |
| pyannote community-1 | CC-BY-4.0 | No | DER DIHARD3 20.2%. CPU. Gives one speaker for each frame, which aligns easily with words. |
| DiariZen | **Weights non-commercial** | No | Do not use. |

### 3.2 Speaker identification (which known person)

Diarization gives clusters with no names. Magi compares the voice embedding
of each cluster with the embeddings of enrolled people.

| Model | Size | EER on VoxCeleb1-O | Licence |
| --- | --- | --- | --- |
| CAM++ | 7.2M | 0.65% | Code Apache-2.0. Check the weights licence. |
| ERes2NetV2 | 17.8M | 0.61% (1.48% at 2 s) | Code Apache-2.0. Check the weights licence. |
| WeSpeaker ResNet34 | 6.6M | 0.72% | Weights CC-BY-4.0 |
| TitaNet-Large | 23M | 0.66% | CC-BY-4.0 |

These error rates are for clean, long recordings. Short audio in a room is
much worse: one study reported 6% at 2 s. For a short wake phrase in a
kitchen, expect an error rate of some percent.

A household is a small closed set (2–8 people). That helps. Guests, children,
and relatives with similar voices are the risk.

- Enroll each person on their own phone, in more than one room.
- Keep more than one embedding for each person.
- Add new embeddings only from turns that the person confirmed. Apple found
  that automatic enrollment can damage a profile.
- Store `speaker_estimate {person, score, method}` on the utterance. Never
  store it as a confirmed identity.

Where to run:

| Place | Diarization | Embeddings |
| --- | --- | --- |
| Android phone | Usually not necessary (one owner) | CAM++ int8 (sherpa-onnx) |
| lab-nas CPU | pyannote community-1, after the conversation | ERes2NetV2 or ResNet34 |
| RTX 4080 | Nemotron-3-Diarization, streaming | Same as lab-nas |

## 4. Wake word and permission to wake a persona

### 4.1 Wake-word engines

| Engine | Licence | Note |
| --- | --- | --- |
| openWakeWord | Code Apache-2.0. **Stock models non-commercial.** | Train our own model for each persona. One Pi 3 core runs 15–20 models. |
| microWakeWord | Apache-2.0 (check) | For ESP32-class boards. 2% false rejects, 0.12 false accepts each hour (author). |
| sherpa-onnx keyword spotting | Apache-2.0 | The keyword is text. No training. CPU and Android. |
| Porcupine | Proprietary | The free tier was reported to stop on 2026-06-30. Do not use. |

### 4.2 The check

How Apple does it ("Personalized Hey Siri", 2018): a detector for all
speakers fires first. Then it compares a speaker vector with the profile of
the user. The result was 1 false accept each month, 5.2% false rejects, and
3.2% accepts of other people.

The flow for Magi:

1. The wake-word model of persona P fires.
2. Magi takes the wake phrase plus a maximum of 1.5 s of the speech after
   it. It calculates one embedding (tens of milliseconds on a phone).
3. It compares the embedding with the enrolled people. Each person and each
   device has a threshold.
4. Policy:
   - High score, and the person can wake P: P wakes as that person.
   - Medium score: P wakes in guest mode. P asks for the name if the
     request needs identity.
   - The person cannot wake P: P does not wake, or gives a neutral refusal.
5. Magi checks again on the full utterance. Longer audio gives a better
   result.
6. The device gives the identity (section 4.3). On a personal device, the
   person is known before the wake word. Voice only confirms that it agrees.
   On a shared device (a kitchen satellite), the voice estimate selects
   personal content only.

The check adds less than 100 ms on CPU.

### 4.3 Why voice cannot authorize

- NIST SP 800-63B-4, section 3.2.3: "Biometrics SHALL only be used as part
  of multi-factor authentication with a physical authenticator" and
  "Biometric comparison based on voice SHALL NOT be used."
- In a 2026 study, cloned voices passed a speaker verifier 43–83% of the
  time at a threshold of 0.01% false accepts. The attacker used 10–30 minutes
  of the target's audio. In a house, people have hours of each other's
  voice, and a phone can replay a recording.
- Google warned that "someone with a similar voice or recording" can
  confirm voice purchases, and set limits on the amount.

#### Identity comes from the device (decision, 2026-10-31)

- Each voice call to the Magi API has a LinkKeys token, as all calls do.
- LinkKeys will soon give **device attestation**. Each voice call carries
  it, so Magi knows that the device is enrolled, and to which person. A
  shared device (for example a kitchen satellite) is enrolled to the
  household, not to one person.
- For a sensitive task, Magi asks for a **second factor** (MFA). The result
  lasts for a time on that device, as a ticket does in Kerberos. While it
  is valid, more sensitive tasks from that device do not ask again.
- The voice estimate (section 3.2) is a signal, not a credential. It can
  select personal content and lower the risk score. It cannot give access.

#### Tiers

A tier tells what each kind of request needs. The household must agree to
the tiers.

| Tier | Examples | Shared device, voice only | Personal enrolled device | Extra |
| --- | --- | --- | --- | --- |
| 0. Public | time, weather, general questions | Yes, any speaker | Yes | None |
| 1. Personal | "my calendar", the voice of the persona | Yes, voice estimate | Yes | Magi says who it thinks speaks |
| 2. Household, can be undone | lights, music, timers, shopping list | Yes, high voice score | Yes | Read back unclear values |
| 3. Private data | read messages, read data of another person | No | Yes | None |
| 4. Sensitive or permanent | money, doors, permissions, delete, send as the person | No | Yes, with a valid second factor | MFA, valid for a time on that device |

This agrees with `docs/prompt-injection.md`: speech in the room can come
from a television or a guest.

## 5. People who talk at the same time

In recorded dinner parties, two people talk at the same time for 18–25% of
the time.

| Method | Status |
| --- | --- |
| Overlap detection (in pyannote, Sortformer, Nemotron diarization) | Works. Use it to mark spans. |
| Separation of one channel (MossFormer2, TF-GridNet, SepFormer) | Good on synthetic mixtures. Bad in real rooms. |
| Extraction of one enrolled speaker | Research. No proven open real-time model. |
| ASR for one target speaker (DiCoW) | Good in CHiME-8. Its licence needs an agreement. |
| Streaming multi-talker Parakeet with Sortformer | Works on GPU. One ASR instance for each speaker. cpWER 37% on AMI far-field. |
| Microphone array with beamforming | Works. Much better than one channel. Needs fixed hardware. |

Rules for Magi:

1. Mark overlapped spans in the utterance graph.
2. Do not run a command from an overlapped span with no confirmation.
3. When a command overlaps other speech, ask: "I heard two people. Who
   wants what?"
4. After the conversation, run a better model on the GPU, and improve the
   stored transcript.

## 6. Review of a transcript by more than one model

| Method | Measured result |
| --- | --- |
| ROVER: align the outputs, vote for each word | Needs different systems. It made the WER worse on one air traffic set (48.6% to 64.4%). |
| LLM correction from many hypotheses (HyPoradise) | Large gains on older, weaker ASR |
| LLM rewrite of Parakeet output | WER **increased** from 10.83% to 20.01% |
| LLM edits only low-confidence spans | Prevents most bad corrections |
| RECOVER (2026): the LLM selects among 5 decodes, and edits only entity spans to items in a list; code checks each edit | Entity WER 14.5–45.7% better (relative). General WER almost the same. |

The design for Magi:

1. **First pass:** Parakeet gives text, word times, and word confidence.
   NeMo entropy-based confidence is 2–4 times better than the maximum
   probability.
2. **Second pass, only for high risk** (section 7): run a different model
   on the same audio. With three systems, 2 of 3 agreeing on a span is a
   majority.
3. **Align** the outputs.
4. When all agree on a span, accept it.
5. When they do not agree on a risky span, the LLM can **select** one of
   the candidates or a household lexicon entry. Code checks that the
   result is one of these. If no candidate is clear, ask the person.
6. Never let an LLM write the whole transcript again. Store the first
   output, each hypothesis, and the result as linked records.

A second pass adds about 100–300 ms. Run it in parallel with the intent
step.

## 7. When the details must be correct

Published practice:

- **Speech applications (IVR):** three bands. Low confidence: ask again.
  Medium: ask "Did you say X?". High: repeat the value in the answer.
- **Alexa and Mycroft design guides:** confirm few things. Confirming all
  things doubles the questions.
- **Medicine:** The Joint Commission requires read-back of verbal orders and
  critical results. Speech recognition in medical dictation had 7.4 errors
  in each 100 words before a person reviewed it.
- **Names and numbers are harder than other words.** Entity error rates are
  often much higher than the general WER.

The rules for Magi:

| Signal | Risk |
| --- | --- |
| Tier 3–4 (section 4.3) | Always high |
| The action cannot be undone, or it affects other people | High |
| Numbers, money, dates, times, durations | Medium to high |
| Names of people, contacts | Medium to high |
| Low word confidence in a value | Medium to high |
| Overlap, distance, noise | Medium |
| Medium speaker score | Medium (identity) |

| Risk | Action | Added time |
| --- | --- | --- |
| Low | Do it. Repeat the value in the answer ("Timer set for 10 minutes"). | 0 |
| Medium | Second pass on the value. If both agree, repeat it in the answer. If not, ask. | 100–300 ms, or one turn |
| High | Read back the exact values. Wait for "yes". | One turn |
| Tier 4 | Read back, then approval on the phone | One turn and one tap |

Read numbers back in two forms: "forty dollars, four zero".

## 8. Inflection

| Method | Status |
| --- | --- |
| A voice and a speed for each persona and mood (Kokoro) | Works now |
| Style instructions ("say it warmly") with Qwen3-TTS | Works on GPU |
| Tags for sounds (`[laugh]`, `<sigh>`) with Chatterbox or Orpheus | Works on GPU. Tags can make noise in the audio. |
| Context from earlier turns (Sesame CSM 1B) | Intonation was flat in an independent test. Needs gated Llama access. |
| SSML | No current open neural TTS in this list supports it |

The plan:

- The LLM gives plain spoken text and a small set of hints:
  `{style: calm|warm|urgent|playful, rate: slow|normal|fast}`.
- Magi maps the hints to what the engine can do. For Kokoro: voice, speed,
  and punctuation.
- Use a fixed list of tags. Remove all other tags.
- Store the hints as data on the spoken utterance, not in its text.
- Test with a small household set (about 50 prompts) on the listening page,
  and with an STT round trip. EmergentTTS-Eval (NeurIPS 2025) is a public
  model for such a test.

## 9. Pronunciation from context

### 9.1 Text normalization before TTS

Kokoro does not know that "4-6" is a range. Magi must make spoken text
before TTS:

1. The LLM writes text for speech: no Markdown, "4 to 6", "readme", units in
   words.
2. A deterministic normalizer does ranges, times, dates, money, units,
   ordinals, and telephone numbers. NeMo text processing (Apache-2.0 code,
   WFST rules) is a base. In one study, an LLM normalizer made about 40%
   fewer errors than production rules, but rules make fewer errors that
   cannot be recovered.
3. A hyphen between digits becomes "to" only in a range context: amounts,
   times, "between", units. Model numbers, dates, and scores stay ("a 3-2
   win"). Do not change all hyphens.
4. Store the original text and the spoken text as linked records.

### 9.2 Lexicon, names, acronyms, homographs

A household lexicon has three columns:

| written | say_as | heard_as |
| --- | --- | --- |
| README | word, /ˈɹidmi/ | "read me" |
| Magi | IPA of the household | "Magei" |
| NAS | word or letters (household choice) | "nass" |

- Kokoro's G2P (misaki, Apache-2.0) accepts inline IPA:
  `[Kokoro](/kˈOkəɹO/)`.
- Add each person name and each persona name. Ask the person to say their
  name once during enrollment.
- **Homographs** ("read", "lead", "live"): misaki does not handle them yet.
  The LLM can mark the pronunciation when it writes the text. A small
  part-of-speech rule handles most cases. In one study, an LLM that selects
  among dictionary pronunciations gave 3.5 points more accuracy.

### 9.3 Learn from the person

1. The person says "No, it is pronounced ...".
2. A phoneme recognizer reads that audio.
3. Magi makes G2P candidates for the word, and selects the one nearest to
   the recognized phonemes.
4. Magi says it again: "Is it ...?". After "yes", Magi saves it.
5. The entry goes into `say_as` for TTS and `heard_as` for STT.

### 9.4 The other direction: STT bias

- sherpa-onnx hotwords work for transducer models with
  `modified_beam_search`. We must test them with Parakeet TDT.
- NeMo TurboBias works with Parakeet and streaming Nemotron. It raised the
  entity F-score from 64.9 to 82.0 (NVIDIA). It needs NeMo on a GPU.
- A cheap method for all models: compare words that have low confidence
  with the `heard_as` column. Replace a word only when it is a close
  phonetic match. Record each replacement.

### 9.5 Status (2026-10-31)

Done in the speech primitive (`primitives/speech/server/spoken.go`) and Magi:

- Markdown removal and the range rule before every synthesis. The text that
  the engine got is in `Audio.spoken`.
- The household lexicon, in PostgreSQL in the speech primitive: `written`,
  `spoken`, `ipa` (misaki phonemes), `heard_as`. Synthesis applies `spoken`
  and `ipa`. Transcription changes `heard_as` forms to `written`.
- Lexicon editors (per installation, `LEXICON_EDITORS`) change the words in
  the web Words tab or with `magi word`. All people can read the words and
  hear text with them.
- Live check on squizzlezig: "README" with the entry is heard back as
  "readme"; an IPA entry reaches Kokoro as phonemes.

Not done: homographs, learning from the person (9.3), STT hotwords (9.4),
and a normalizer for dates, money, and units.

## 10. Licences

Permissive and useful: Kokoro, misaki, Qwen3-TTS (Apache-2.0); Silero VAD,
Moonshine v2, Chatterbox (MIT); Smart Turn v3 (BSD-2); Parakeet v3, pyannote
community-1, TitaNet, WeSpeaker VoxCeleb weights (CC-BY-4.0).

Read before use: NVIDIA Open Model License (custom), OpenMDW-1.1 (permissive,
not OSI approved yet), 3D-Speaker weights (licence not stated).

Do not use: openWakeWord stock models, DiariZen weights, Fish and OpenAudio
weights (non-commercial); LiveKit turn-detector weights (custom); Piper and
espeak-ng in the same process (GPL-3.0).

**A note about Magi now:** the Kokoro environment on lab-nas has
`espeakng_loader` and `phonemizer`. misaki uses espeak-ng for words that it
does not know. Thus the TTS engine loads a GPL-3.0 library in its process.
Magi does not distribute this environment: the host setup tool
(`primitives/speech/engine/install.sh`) installs it on the host. Thus this
is not a distribution problem. If Magi ever distributes the environment,
turn the fallback off (`fallback=None`) and use the lexicon, or run
espeak-ng as a separate process.

## 11. Architecture

```
Android phone / Pi satellite       lab-nas (CPU)                   RTX 4080 (GPU)
microphone with echo cancel        speech service (Go)             LLM 9B, streaming
Silero VAD                         Smart Turn v3                   later, when measured:
wake word for each persona         Parakeet (last segment)          Nemotron streaming STT
speaker embedding (CAM++)          Kokoro (each clause)             Nemotron-3-Diarization
device key (LinkKeys)  -- out -->  normalizer and lexicon           Qwen3-TTS 0.6B
playback, barge-in    <-- audio -- speaker identification          second-pass STT
                                   risk policy, read-back
                                   pyannote (after the conversation)
```

All speech goes through the Magi API with a token. Phones and satellites
connect out to Magi. Memory plan for the GPU (to measure): LLM 7 GB,
streaming STT about 2 GB, diarization about 0.5 GB, Qwen3-TTS 0.6B 2–3 GB.

## 12. Roadmap

| Phase | Work | Why |
| --- | --- | --- |
| Host setup | A tool that makes a host a speech or model host: it downloads the pinned models (for example from Hugging Face), checks their hashes, and makes the environments. The tool is AI driven, with a local CPU model (see `docs/host-setup.md`). Today `engine/install.sh` needs environments that are already on the host. | Magi gives tools, not images. |
| 0. Correct speech | **Done:** range rule, Markdown removal, household lexicon with editors, `heard_as` match. **To do:** regression prompts in the live round-trip test. | People hear these errors every day. No new model. |
| 1. Low latency | Clauses to Kokoro, streamed to the client with word times. A voice session in the API. Silero VAD and Smart Turn. Barge-in and the interruption record (section 2.3.1). Time for each stage. | The largest gain for the least work. |
| 2. Streaming STT | Measure nemotron-speech-streaming-en int8 on the lab-nas CPU. Use the GPU if it is too slow. | No published CPU number. |
| 3. Who speaks | Device attestation on voice calls (LinkKeys). Enrollment on Android. Speaker estimates on utterances. Wake word for each persona. Tiers. Second factor with a time limit on the device. | The household must agree to the tiers first. |
| 4. Many people, precision | Diarization. Overlap marks. Second pass, LLM selection, read-back. | Needs phase 3 identities. |
| 5. Expression | Hints mapped to Kokoro. Test Qwen3-TTS 0.6B on GPU. Household test set. | After speech is correct and fast. |

Watch: full-duplex models with tools (NemotronLabs VoiceChat, 2026-09), a
sherpa-onnx build of the Nemotron unified model, and the OSI decision on
OpenMDW-1.1.

## 13. Sources

All sources were read on 2026-10-31. Dates are publication dates.

Streaming and latency:
- Daily, "Building Voice Agents with NVIDIA Open Models", 2026-01-05.
  https://www.daily.co/blog/building-voice-agents-with-nvidia-open-models/
- NVIDIA, nemotron-labs-unified-asr-0.6b, 2026-10-27.
  https://huggingface.co/nvidia/nemotron-labs-unified-asr-0.6b
- NVIDIA, "Scaling Real-Time Voice Agents with Cache-Aware Streaming ASR",
  2026-01-05.
  https://huggingface.co/blog/nvidia/nemotron-speech-asr-scaling-voice-agents
- sherpa-onnx, Nemotron streaming models.
  https://k2-fsa.github.io/sherpa/onnx/nemo/nemotron-streaming.html
- NVIDIA, parakeet-tdt-0.6b-v3. https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3
- Kyutai STT. https://kyutai.org/stt
- Moonshine v2, arXiv 2602.12241, 2026-02. https://arxiv.org/html/2602.12241v1
- Kokoro-82M. https://huggingface.co/hexgrad/Kokoro-82M
- Kokoro-FastAPI (chunking). https://github.com/remsky/Kokoro-FastAPI
- Qwen3-TTS technical report, arXiv 2601.15621, 2026-01.
  https://arxiv.org/html/2601.15621
- Kyutai TTS. https://kyutai.org/next/tts
- Chatterbox. https://github.com/resemble-ai/chatterbox
- Piper licence change (v1.7.0, GPL-3.0).
  https://github.com/OHF-Voice/piper1-gpl/releases
- Picovoice, on-device TTS benchmark, 2026-07-14.
  https://picovoice.ai/blog/on-device-tts/
- Pipecat Smart Turn. https://github.com/pipecat-ai/smart-turn
- Silero VAD. https://github.com/snakers4/silero-vad
- LiveKit turn detector.
  https://docs.livekit.io/agents/logic/turns/turn-detector
- Android audio pre-processing.
  https://source.android.com/docs/core/audio/implement-pre-processing
- NemotronLabs VoiceChat, arXiv 2609.21967, 2026-09.
  https://arxiv.org/pdf/2609.21967

Speakers and wake words:
- NVIDIA, Nemotron-3-Diarization, 2026-09-23.
  https://huggingface.co/nvidia/Nemotron-3-Diarization
- NVIDIA, diar_streaming_sortformer_4spk-v2.1.
  https://huggingface.co/nvidia/diar_streaming_sortformer_4spk-v2.1
- pyannote, speaker-diarization-community-1, 2025-09.
  https://huggingface.co/pyannote/speaker-diarization-community-1
- BUT-FIT, DiariZen. https://huggingface.co/BUT-FIT/diarizen-wavlm-large-s80-md
- 3D-Speaker. https://github.com/modelscope/3D-Speaker
- WeSpeaker pretrained models.
  https://github.com/wenet-e2e/wespeaker/blob/master/docs/pretrained.md
- NVIDIA, TitaNet-Large.
  https://huggingface.co/nvidia/speakerverification_en_titanet_large
- ERes2NetV2, arXiv 2406.02167. https://arxiv.org/abs/2406.02167
- openWakeWord. https://github.com/dscripka/openWakeWord
- microWakeWord. https://github.com/kahrendt/microWakeWord
- Picovoice free tier end (community report).
  https://community.home-assistant.io/t/fyi-picovoice-confirmed-free-tier-accesskeys-will-stop-working-after-june-30-2026/1012744
- Apple, "Personalized Hey Siri", 2018-04-16.
  https://machinelearning.apple.com/research/personalized-hey-siri
- Google Assistant Voice Match. https://support.google.com/assistant/answer/9071681
- NIST SP 800-63B-4, section 3.2.3.
  https://pages.nist.gov/800-63-4/sp800-63b.html
- ASVspoof 5, arXiv 2408.08739. https://arxiv.org/pdf/2408.08739
- Hong et al., deepfake speech against voice biometrics, arXiv 2601.02914,
  2026-01. https://arxiv.org/pdf/2601.02914

Overlap and transcript review:
- NOTSOFAR-1, arXiv 2501.17304. https://arxiv.org/html/2501.17304v2
- DiCoW, arXiv 2501.00114. https://arxiv.org/abs/2501.00114
- NVIDIA, multitalker-parakeet-streaming-0.6b-v1.
  https://huggingface.co/nvidia/multitalker-parakeet-streaming-0.6b-v1
- ClearerVoice-Studio (MossFormer2).
  https://github.com/modelscope/ClearerVoice-Studio
- HyPoradise, arXiv 2309.15701. https://arxiv.org/abs/2309.15701
- RECOVER, arXiv 2603.16411, 2026-03-17. https://arxiv.org/html/2603.16411
- Confidence-guided correction, arXiv 2509.25048.
  https://arxiv.org/html/2509.25048
- Entropy-based word confidence, arXiv 2212.08703.
  https://arxiv.org/pdf/2212.08703
- ROVER with quality estimation, arXiv 1706.07238.
  https://arxiv.org/abs/1706.07238

Confirmation practice:
- Nuance, confidence scores.
  https://docs.nuance.com/nvp-for-speech-suite/appdev/rc-scores.html
- Mycroft, confirmations.
  https://mycroft-ai.gitbook.io/docs/skill-development/voice-user-interface-design-guidelines/interactions-and-guidelines/confirmations
- Zhou et al., errors in dictated clinical documents, JAMA Netw Open 2018.
  https://psnet.ahrq.gov/issue/analysis-errors-dictated-clinical-documents-assisted-speech-recognition-software-and
- AHRQ PSNet, "Say it again" (read-back). https://psnet.ahrq.gov/web-mm/say-it-again

Prosody and pronunciation:
- EmergentTTS-Eval, arXiv 2505.23009. https://arxiv.org/abs/2505.23009
- Sesame CSM 1B. https://huggingface.co/sesame/csm-1b
- misaki. https://github.com/hexgrad/misaki
- GPT-based text normalization, arXiv 2309.13426.
  https://arxiv.org/abs/2309.13426
- NeMo text processing. https://github.com/NVIDIA/NeMo-text-processing
- LLM in-context G2P, arXiv 2411.07563. https://arxiv.org/html/2411.07563v1
- sherpa-onnx hotwords. https://k2-fsa.github.io/sherpa/onnx/hotwords/index.html
- NeMo word boosting.
  https://docs.nvidia.com/nemo/speech/latest/asr/asr_customization/word_boosting.html

Limits: we did not read the Open ASR leaderboard page. We did not confirm
the licences of microWakeWord, the 3D-Speaker weights, and the Dia weights
from primary text. No published CPU speed exists for the Nemotron streaming
models. Vendor numbers were measured by the vendor on its own model.
