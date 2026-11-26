#!/usr/bin/env bash
# Gets the speech library and the voice models of the Android app
# (android/voice-pins.txt) and checks the SHA-256 digest of each download.
#   tools/fetch-android-voice.sh [DIR]
# DIR is the voice directory of the build: MAGI_ANDROID_VOICE, or
# ~/.cache/magi/android-voice. A file that is there with the right pins is
# not downloaded again. Needs curl, tar, and sha256sum.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
pins=$root/android/voice-pins.txt
dir=${1:-${MAGI_ANDROID_VOICE:-$HOME/.cache/magi/android-voice}}
mkdir -p "$dir/downloads"
if [[ -f $dir/voice-pins.txt ]] && cmp -s "$pins" "$dir/voice-pins.txt"; then
  echo "$dir is current" >&2
  exit 0
fi
rm -f "$dir/voice-pins.txt"
while read -r sum url member out; do
  [[ -z $sum || $sum == \#* ]] && continue
  file=$dir/downloads/${url##*/}
  if [[ ! -f $file ]] || ! echo "$sum  $file" | sha256sum -c --status; then
    echo "get ${url##*/}" >&2
    curl -fsSL -o "$file.part" "$url"
    echo "$sum  $file.part" | sha256sum -c --status || { echo "wrong digest: $url" >&2; rm -f "$file.part"; exit 1; }
    mv "$file.part" "$file"
  fi
  mkdir -p "$(dirname "$dir/$out")"
  if [[ $member == - ]]; then
    cp "$file" "$dir/$out"
  else
    tar -xjf "$file" -O "$member" > "$dir/$out.part"
    mv "$dir/$out.part" "$dir/$out"
  fi
done < "$pins"
# The spotter needs a list of names in a file. The app sets the names of
# the personas when it starts (Senses.names); this list is the start.
printf '%s\n' '▁MA G I @MAGI' '▁A R CH IM ED ES @ARCHIMEDES' '▁BE AT RI CE @BEATRICE' > "$dir/assets/voice/kws/keywords.txt"
cp "$pins" "$dir/voice-pins.txt"
du -sh "$dir/sherpa-onnx.aar" "$dir/assets" >&2
