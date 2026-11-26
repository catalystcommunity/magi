# Android client

A small Android app for the Magi API. It uses only `IdentityService` (sign
in) and `MagiService` (conversation, voice, reviews, tasks). It is one replaceable client,
like the web and command-line clients.

- Kotlin, Android framework views. No AndroidX and no Google Play services.
  The only runtime library other than the Kotlin standard library is
  sherpa-onnx with ONNX Runtime, for the voice models on the phone
  ("Voice models").
- The API client is generated from `contracts/identity.csil` and
  `contracts/magi.csil` into `clients/kotlin/` (`tools/gen.sh`).
- `minSdk` 29 (Android 10).
- Hands-free talk belongs to the app process, not to the screen
  (`Talk.kt`). It works with the app in the background. See "Voice".
- Notices: `NoticeService` is a foreground service of type
  `remoteMessaging` with a quiet notification ("Ready for notices"). It
  waits for notices with a long poll that connects out to Magi, and it
  refreshes the sign in. It starts with the app and after the phone starts.
  While hands-free talk is on, it also has the type `microphone`, and the
  notification says "Listening for a name".
- The app opens on the tab that the person used last, with the
  conversation of the last persona, and with the microphone on or off as
  the person left it.

## Features

| Screen | Use |
| --- | --- |
| Sign in | Server URL and LinkKeys identity. The browser opens for LinkKeys and then comes back to the app. |
| Talk | One conversation with each persona (Magi, Engineering manager, and others); no chats. Text messages, and the microphone button for hands-free talk: the microphone stays on, also with the app in the background. The name of a persona ("Hey, Archimedes") opens a conversation; then you talk with no name, and you can talk over a persona to stop it (a short "mm-hm" does not stop it). "Goodbye" or "End of line" ends the conversation. A reply to a voice message is spoken. "Speak replies" also speaks replies to text. A message that relates to an earlier one shows an "about" line; tap it to go to that message. Long-press a message to copy it, to see its links and who can read it, and to correct them. |
| Notices | Notices of the personas and of tasks (docs/attention.md). While the app is open, a notice shows at the top (the two newest; a line opens the others), and Magi says it after a tone: at once when it is urgent, else at the next pause. While the app is closed, a background service shows it as a system notification; tap opens the conversation, swipe dismisses it on all devices. |
| Review | The questions of the librarian, when it is not sure how a message relates to an earlier one or who can read it |
| Network | Admins only: requests of tasks that the egress proxy blocked for review. Each decision (allow the host, allow the path, block, or dismiss) is a rule for the next such requests. |
| Tasks | Status of tasks, the diff, and the decisions of a waiting task (for example approve or reject) |
| Admin | Admins only: the runs of all people with their steps, data, diff, and outputs; the conversations of all people, with the turn log of a reply; your knowledge graph; the runners. Magi checks the role for each call. |
| Menu | Speak replies, the wake word, "Your voice" (the voice setup), the assistant app, the voice of the persona (admins), the theme, "Check for updates" (asks Magi for a newer build now), "About" (the version, the time of the build, the server, and your identity), and the sign out. |

## Voice

`Talk.kt` has the microphone, the conversation, and the spoken replies.
The notice service keeps the process, so they work with no open screen.
`Ear.kt` is the microphone. It has two states.

- **It waits for a name.** The phone is not in call mode, so other apps
  sound as usual. For a quiet room, the phone computes only the loudness
  of each 20 ms. A loud stretch goes to the speech detector and to the
  spotter of the wake names. The wake names are the names of the
  personas. Speech with no name stays on the phone and is dropped.
- **It talks.** A conversation is open, or a persona speaks. The phone is
  in call mode on its loudspeaker, so its echo canceller removes the
  persona voice. Each turn goes to Magi as WAV (16 kHz).

After a name, and for each turn of a conversation, the phone makes a voice
print of the speech (`VoicePrints.kt`) and compares it with the prints of
the person:

| Result | What the phone does |
| --- | --- |
| The person | The turn goes to Magi. |
| Not sure (noise, or two voices) | The turn goes to Magi with the score. Magi extracts the voice of the person and looks again. Magi drops the turn when it is still not sure. |
| Another person | The phone drops the turn. Nothing goes to Magi. |
| A persona voice from a loudspeaker | The phone drops the turn. This is the echo of the phone, or a second device in the room. |

- The phone learns the persona voices from the speech that it plays.
- While a persona speaks, only the voice of the person makes the persona
  quieter and can stop it.
- The voice of the person selects what Magi hears. It gives no permission.
  The identity is the sign in of the phone.
- **Voice setup** (menu, "Your voice"): the person reads six lines in a
  quiet room. The phone keeps a voice print of each line. The prints and
  the audio of the lines go to Magi, so the other devices of the person
  and the extraction of Magi have them. A new phone gets the prints from
  Magi. With no voice setup, the phone cannot tell voices apart, and each
  person who says a name is heard.
- The phone adds a print of a turn that is the person for certain, at most
  one in 10 minutes, so the prints follow the voice of the person.

### Assistant app

Android gives the microphone to an app in the background only in two
cases: the app started its microphone service while it was on the screen,
or the app is the digital assistant app of the phone.

- The person makes Magi the assistant app in the settings of the phone
  (Apps, Default apps, Digital assistant app). The menu of the app opens
  that setting. An app cannot take the role by itself.
- As the assistant app, Magi listens after the phone starts, with no open
  of the app. The assistant gesture of the phone opens a conversation
  that needs no name.
- When Magi is not the assistant app, the microphone works in the
  background until Android stops the app. Then the notification says
  "Open Magi to turn the microphone on".
- Magi does not use the hotword hardware of the phone. Android gives it
  only to apps of the system image. The wake names run on the processor.
  Thus the battery use is higher than that of the assistant of the system
  image. The app reports it; see the next section.

### Health report

Each 5 minutes the app sends the battery level, the charging state, and
the counters of the microphone to Magi (`put-device`, `health`): the
seconds that the microphone was on, the seconds of loud sound (the models
ran), the wake names, the turns, and the turns that the voice print
dropped. Magi computes the battery use for each hour with the microphone
on and off (`docs/attention.md`).

## Voice models

The phone runs three models with sherpa-onnx (`Senses.kt`). They are not
in the repository. `tools/fetch-android-voice.sh` downloads them and
checks the SHA-256 digest of each file (`android/voice-pins.txt`).

| Model | Use | Size | License |
| --- | --- | --- | --- |
| Silero VAD | Is a loud frame speech? | 0.6 MB | MIT |
| Zipformer keyword spotter (GigaSpeech, 3.3 million parameters) | The wake names. A name is text, so a new persona name needs no training. | 6 MB | Apache-2.0 |
| 3D-Speaker ERes2Net (English, VoxCeleb) | The voice print (192 numbers) | 26 MB | Apache-2.0 |

- The library is sherpa-onnx 2.0.1 with ONNX Runtime (Apache-2.0 and
  MIT), for arm64. The package of the app is about 60 MB.
- `Unigram.kt` splits a name into the pieces of the spotter, as
  SentencePiece does. A name with a digit or a letter with an accent is no
  wake name.
- The limits of the voice print and of the spotter are constants in
  `VoicePrints.kt` and `Senses.kt`, with the measurement that they come
  from.
- A build with no models has no spotter and no voice print: Magi finds the
  name in the words of each turn.

## Sign in

1. The app calls `begin-login` with `return_url`
   `community.catalyst.magi:/signed-in` (an RFC 8252 private-use scheme). It
   keeps `login_id` in its private preferences.
2. The browser opens the sign-in link. The person clicks Continue and signs
   in with LinkKeys.
3. The browser opens `community.catalyst.magi:/signed-in?code=...`. Android
   gives it to the app.
4. The app calls `poll-login` with `login_id` and the code, and keeps the
   token. The token lasts 12 hours. The app refreshes it when less than 2
   hours are left, while the login session is active (7 days).

The code is useless without `login_id`. Thus another app that claims the
same scheme cannot sign in as the person.

## Look

- The theme is dark. The menu of a page has the choice: dark, light, or
  the theme of the phone.
- `Ui.kt` has the colors (`Palette`) and the widgets. New pages use them.
- The conversation is a list of bubbles. The bubbles of the person are on
  the right. A long press on a bubble copies the text and shows its links.

## Updates

Magi keeps the newest app build. The app asks for it when it opens, about
once an hour in the background (a `JobScheduler` job, any network), and
when Magi sends the notice of a new build.

- The app downloads a build with a higher version code and checks it
  against its SHA-256 digest. It does not install the build.
- The app then shows a system notification ("Magi update is ready") and a
  line at the top of the conversation. "Check for updates" in the menu
  does the same check at once.
- The person confirms, and only then the app installs the build with
  `PackageInstaller`. The phone can ask for one more confirmation, or
  show a warning of Google Play Protect, because the build is not from an
  app store. The person expects it at that moment.
- The app tells Magi its version, and why an install failed
  (`docs/attention.md`, section 7).
- The app is the installer of record of itself when the first build was
  installed that way, and then Android asks no question of its own:

  ```sh
  P=community.catalyst.magi.dev
  adb push android/app/build/outputs/apk/debug/app-debug.apk /data/local/tmp/magi.apk
  adb shell pm install -r -i $P /data/local/tmp/magi.apk
  adb shell rm /data/local/tmp/magi.apk
  adb shell appops set $P REQUEST_INSTALL_PACKAGES allow
  ```

  `pm install -i` needs a build of the app already on the phone. On a new
  phone, use `adb install` first, then the commands above.
- Updates keep the signing key: build on the same machine (the debug key in
  `~/.android/debug.keystore`), or Android refuses the update.
- Publish a build (operator, see `magi/README.md`):

  ```sh
  tools/build-android.sh            # prints "version CODE (NAME)" and the APK
  magi app-release CODE NAME android/app/build/outputs/apk/debug/app-debug.apk
  ```

This app is not for an app store. Build it yourself.

## Build

```sh
GRADLE=/path/to/gradle-8.11.1/bin/gradle JAVA_HOME=/path/to/jdk17 \
  tools/build-android.sh
```

- Run `tools/fetch-android-voice.sh` first, one time, and again when
  `android/voice-pins.txt` changes. It puts the speech library and the
  voice models in `~/.cache/magi/android-voice` (or `MAGI_ANDROID_VOICE`).
  The build stops when they are missing or old.
- The script runs the unit tests and builds
  `android/app/build/outputs/apk/debug/app-debug.apk`.
- `-PmagiEmulator` adds the library for an x86_64 emulator.
- `ANDROID_HOME` is the Android SDK (default `~/Android/Sdk`). It must have
  platform 36.
- `MAGI_GRADLE_OFFLINE=1` builds only from the local Gradle cache.
- The repository has no Gradle wrapper, because the wrapper is a binary jar.
  Use Gradle 8.11 or later.

Install the debug build on a phone with USB debugging:

```sh
adb install -r android/app/build/outputs/apk/debug/app-debug.apk
```

The debug build has the application ID `community.catalyst.magi.dev`.

## Tests

`RpcTest` runs on the JVM. It sends calls through the HTTP carrier and the
generated codec to a fake server, and checks the token, the user agent, and
the error codes. `UnigramTest` compares the pieces of names with
SentencePiece. `VoicePrintsTest` has the decision of the voice print.
`VadTest` has the turn detector.

To test the voice path with no person and no microphone (an emulator), a
debug build can take its sound from files (`Mic.kt`):

```sh
P=community.catalyst.magi.dev
adb shell run-as $P mkdir files/mic-test          # from now on, the sound is the files
adb push hey.wav /data/local/tmp/hey.wav
adb shell run-as $P cp /data/local/tmp/hey.wav files/mic-test/hey.wav
adb logcat -s MagiEar MagiTalk MagiMic            # the decision for each turn, with the scores
adb shell run-as $P rm -r files/mic-test          # the microphone again
```

The app plays each WAV file one time at the speed of speech and deletes
it. Between files the sound is silence. Only USB debugging can make the
directory, and a release build does not look for it. The log has the
decision and the score of each turn and no audio.
