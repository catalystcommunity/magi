#!/usr/bin/env bash
# Build Magi images and push them to the Magi registry.
#   tools/build-images.sh [name...]     (default: all)
# Pushes 127.0.0.1:55000/magi/<name>:<tag>. Nodes pull localhost:30500/magi/<name>:<tag>.
# Writes the image references to .build/images.env.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
tag=${MAGI_TAG:-$(date -u +%Y%m%d%H%M%S)}
push=${MAGI_PUSH_REGISTRY:-127.0.0.1:55000}
pull=${MAGI_PULL_REGISTRY:-localhost:30500}
"$root/tools/registry-forward.sh" >/dev/null

# name -> "go MODULE CMD" or "file DOCKERFILE"
declare -A images=(
  [artifacts]="go primitives/artifacts artifacts"
  [conversation]="go primitives/conversation conversation"
  [knowledge]="go primitives/knowledge knowledge"
  [workflow]="go primitives/workflow workflow"
  [execution]="file primitives/execution/Dockerfile"
  [speech]="go primitives/speech speech"
  [identity]="go primitives/identity identity"
  [magi]="go magi magi-server"
  [source]="file primitives/source/Dockerfile"
  [agent]="file primitives/agent/Dockerfile"
  [release]="file primitives/release/Dockerfile"
)
names=("$@")
[[ ${#names[@]} -eq 0 ]] && names=("${!images[@]}")
mkdir -p .build
touch .build/images.env
for n in "${names[@]}"; do
  read -r kind a b <<<"${images[$n]}"
  ref="$push/magi/$n:$tag"
  if [[ $kind == go ]]; then
    # Static binaries are built on the host with its module and build caches.
    ctx="$root/.build/img/$n"
    rm -rf "$ctx" && mkdir -p "$ctx"
    (cd "$a" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$ctx/app" "./cmd/$b")
    printf 'FROM gcr.io/distroless/static-debian12:nonroot\nCOPY app /app\nENTRYPOINT ["/app"]\n' > "$ctx/Dockerfile"
    docker build -q -t "$ref" "$ctx" >/dev/null
  else
    docker build -q -f "$a" -t "$ref" . >/dev/null
  fi
  docker push -q "$ref" >/dev/null
  digest=$(docker inspect --format '{{index .RepoDigests 0}}' "$ref" | sed 's/.*@//')
  key=$(echo "$n" | tr a-z- A-Z_)
  sed -i "/^IMAGE_$key=/d" .build/images.env
  echo "IMAGE_$key=$pull/magi/$n:$tag@$digest" >> .build/images.env
  echo "built $n -> $pull/magi/$n:$tag"
done
