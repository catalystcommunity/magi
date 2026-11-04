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
# The version code is the minute of the build (since 2020), so each build
# is newer than the last; the self-update needs that. The name has the commit.
code=$(( ($(date -u +%s) - 1577836800) / 60 ))
name="0.1.0-$(git -C "$root" rev-parse --short=12 HEAD)"
[[ -z $(git -C "$root" status --porcelain) ]] || name+="-dev"
args+=(-PmagiVersionCode="$code" -PmagiVersionName="$name")
echo "version $code ($name)" >&2
"$gradle" "${args[@]}" "$@"
ls "$root"/android/app/build/outputs/apk/*/*.apk 2>/dev/null || true
