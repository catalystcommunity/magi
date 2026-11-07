# Android client

A small Android app for the Magi API. It uses only `IdentityService` (sign
in) and `MagiService` (conversation, voice, reviews, tasks). It is one replaceable client,
like the web and command-line clients.

- Kotlin, Android framework views. No AndroidX, no Google Play services, and
  no runtime libraries other than the Kotlin standard library.
- The API client is generated from `contracts/identity.csil` and
  `contracts/magi.csil` into `clients/kotlin/` (`tools/gen.sh`).
- `minSdk` 29 (Android 10).
- Hands-free talk records 16 kHz PCM with the voice-call source and sends WAV.
  While it is on, the phone is in communication mode on its loudspeaker and
  speech plays as a voice call, so the echo canceller of the phone removes
  the persona voice from the microphone. The voice activity detector
  (`Vad.kt`) is the same as the one of the web client.
- Notices: `NoticeService` is a foreground service of type
  `remoteMessaging` with a quiet notification ("Ready for notices"). It
  waits for notices with a long poll that connects out to Magi, and it
  refreshes the sign in. It starts with the app and after the phone starts.
  While hands-free talk is on, it also has the type `microphone` (started
  from the open app), so the microphone works with the screen off.

## Features

| Screen | Use |
| --- | --- |
| Sign in | Server URL and LinkKeys identity. The browser opens for LinkKeys and then comes back to the app. |
| Talk | One conversation with each persona (Magi, Engineering manager, and others); no chats. Text messages, and Talk for hands-free talk: the microphone stays on, you talk at any time, and you can talk over a persona to stop it (a short "mm-hm" does not stop it). A reply to a voice message is spoken. "Speak replies" also speaks replies to text. A message that relates to an earlier one shows an "about" line; tap it to go to that message. Long-press a message to see its links and who can read it, and to correct them. |
| Notices | Notices of the personas and of tasks (docs/attention.md). While the app is open, a notice shows at the top, and Magi says it after a tone: at once when it is urgent, else at the next pause. While the app is closed, a background service shows it as a system notification; tap opens the conversation, swipe dismisses it on all devices. |
| Review | The questions of the librarian, when it is not sure how a message relates to an earlier one or who can read it |
| Network | Admins only: requests of tasks that the egress proxy blocked for review. Each decision (allow the host, allow the path, block, or dismiss) is a rule for the next such requests. |
| Tasks | Status of tasks, the diff, and the decisions of a waiting task (for example approve or reject) |

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

## Updates

Magi keeps the newest app build. The app checks for it when it opens and
about once an hour in the background (a `JobScheduler` job, any network).
A build with a higher version code is downloaded, checked against its
SHA-256 digest, and installed with `PackageInstaller`.

- Updates install without a question only when the app is the installer
  of record of itself. Install the first build over USB with the app as
  the installer, and let it install apps:

  ```sh
  P=community.catalyst.magi.dev
  adb push android/app/build/outputs/apk/debug/app-debug.apk /data/local/tmp/magi.apk
  adb shell pm install -r -i $P /data/local/tmp/magi.apk
  adb shell rm /data/local/tmp/magi.apk
  adb shell appops set $P REQUEST_INSTALL_PACKAGES allow
  ```

  `pm install -i` needs a build of the app already on the phone. On a new
  phone, use `adb install` first, then the commands above. With a plain
  `adb install -r`, Android asks you to confirm the first update; after
  that update, the app is the installer of record.
- A confirmation that Android asks for while the app is in the background
  waits until you open the app.
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

- The script runs the unit tests and builds
  `android/app/build/outputs/apk/debug/app-debug.apk`.
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
the error codes.
