# Build from the repository root:
#   docker build -f primitives/release/Dockerfile -t localhost:30500/magi/release:<tag> .
FROM rust:1.89-bookworm AS build
WORKDIR /src
COPY primitives/release/Cargo.toml primitives/release/Cargo.lock ./
# Cache dependencies in a separate layer.
RUN mkdir src && echo 'fn main() {}' > src/main.rs && touch src/lib.rs \
 && cargo build --release --locked && rm -rf src
COPY primitives/release/src ./src
COPY primitives/release/testdata ./testdata
RUN touch src/main.rs src/lib.rs && cargo build --release --locked

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /src/target/release/magi-release /usr/local/bin/magi-release
USER 65532:65532
ENV MAGI_LISTEN=:8080 MAGI_RELEASE_DATA=/var/lib/magi-release
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/magi-release"]
