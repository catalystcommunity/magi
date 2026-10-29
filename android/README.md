# Android client

A small Android app for the Magi API. It uses only `IdentityService` (sign
in) and `MagiService` (chats, voice, tasks). It is one replaceable client,
like the web and command-line clients.

- Kotlin, Android framework views. No AndroidX, no Google Play services, and
  no runtime libraries other than the Kotlin standard library.
- The API client is generated from `contracts/identity.csil` and
  `contracts/magi.csil` into `clients/kotlin/` (`tools/gen.sh`).
- `minSdk` 29 (Android 10): the recorder writes Ogg Opus, which the speech
  service reads directly.

## Features

| Screen | Use |
| --- | --- |
| Sign in | Server URL and LinkKeys identity. The browser opens for LinkKeys and then comes back to the app. |
| Chats | List of chats, and a new chat |
| Chat | Text messages, and Talk/Stop to send a voice message. A reply to a voice message is spoken. "Speak replies" also speaks replies to text. |
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
