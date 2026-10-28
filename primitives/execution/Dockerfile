# Build from the repository root:
#   docker build -f primitives/execution/Dockerfile -t magi-execution .
# The image holds the service and static envd builds for amd64 and arm64.
# Environments download envd from GET /envd/linux-<arch>.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY lib/ lib/
COPY primitives/execution/ primitives/execution/
WORKDIR /src/primitives/execution
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/execution ./cmd/execution \
 && for a in amd64 arm64; do GOOS=linux GOARCH=$a go build -trimpath -ldflags="-s -w" -o /out/envd/envd-linux-$a ./cmd/envd; done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/execution /usr/local/bin/execution
COPY --from=build /out/envd/ /usr/local/lib/magi/envd/
ENV MAGI_EXEC_ENVD_DIR=/usr/local/lib/magi/envd
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/execution"]
