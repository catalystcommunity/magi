#!/usr/bin/env bash
# Test and build the Android client (android/). Prints the APK path.
#   tools/build-android.sh [gradle tasks...]   (default: testDebugUnitTest assembleDebug)
# Needs Gradle 8.11 or later (GRADLE, or gradle on PATH), a JDK 17 (JAVA_HOME),
# and the Android SDK with platform 36 (ANDROID_HOME, default ~/Android/Sdk).
# MAGI_GRADLE_OFFLINE=1 builds only from the local Gradle cache.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
gradle=${GRADLE:-gradle}
export ANDROID_HOME=${ANDROID_HOME:-$HOME/Android/Sdk}
args=(-q -p "$root/android")
[[ ${MAGI_GRADLE_OFFLINE:-} == 1 ]] && args+=(--offline)
(( $# )) || set -- testDebugUnitTest assembleDebug
"$gradle" "${args[@]}" "$@"
ls "$root"/android/app/build/outputs/apk/*/*.apk 2>/dev/null || true
