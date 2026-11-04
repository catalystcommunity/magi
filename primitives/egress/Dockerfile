# Build from the repository root:
#   docker build -f primitives/egress/Dockerfile -t localhost:30500/magi/egress:<tag> .
# The image has the proxy and the default plugins. /opt/magi/plugins/SHA256SUMS
# gives the digests for the plugin grants of the policy.
FROM rust:1.98-bookworm AS build
RUN rustup target add wasm32-wasip2
WORKDIR /src/primitives/egress
COPY contracts/egress.wit /src/contracts/egress.wit
COPY primitives/egress/Cargo.toml primitives/egress/Cargo.lock ./
COPY primitives/egress/plugins/Cargo.toml primitives/egress/plugins/Cargo.lock ./plugins/
# Cache dependencies in a separate layer.
RUN mkdir src && echo 'fn main() {}' > src/main.rs && touch src/lib.rs \
 && cargo build --release --locked && rm -rf src
COPY primitives/egress/src ./src
COPY primitives/egress/plugins ./plugins
RUN touch src/main.rs src/lib.rs && cargo build --release --locked \
 && cd plugins && cargo build --release --locked --target wasm32-wasip2 \
 && mkdir -p /out/plugins && cp target/wasm32-wasip2/release/placeholders.wasm target/wasm32-wasip2/release/magi_review.wasm /out/plugins/ \
 && cd /out/plugins && sha256sum *.wasm > SHA256SUMS

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /src/primitives/egress/target/release/magi-egress /usr/local/bin/magi-egress
COPY --from=build /out/plugins /opt/magi/plugins
USER 65532:65532
ENV MAGI_LISTEN=:8080 MAGI_EGRESS_PROXY_LISTEN=:3128
EXPOSE 8080 3128
ENTRYPOINT ["/usr/local/bin/magi-egress"]
