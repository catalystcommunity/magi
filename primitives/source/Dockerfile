# Build from the repository root:
#   docker build -f primitives/source/Dockerfile -t magi-source .
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY lib/ lib/
COPY primitives/source/ primitives/source/
WORKDIR /src/primitives/source
RUN GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/source ./cmd/source

# git 2.39 in bookworm supports "merge-tree --write-tree" (2.38 and later).
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /var/lib/magi --create-home magi \
 && mkdir -p /var/lib/magi/source && chown -R magi:magi /var/lib/magi
COPY --from=build /out/source /usr/local/bin/source
USER 10001:10001
ENV MAGI_SOURCE_DIR=/var/lib/magi/source HOME=/var/lib/magi
VOLUME /var/lib/magi/source
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/source"]
