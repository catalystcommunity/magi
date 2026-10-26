ARG PYTHON_IMAGE=python@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a
FROM ${PYTHON_IMAGE} AS workflows
COPY workflows /usr/local/bin/workflows
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/workflows"]

FROM ${PYTHON_IMAGE} AS model-build
RUN apk add --no-cache cmake ninja g++ linux-headers
ADD llama.tar /src/
RUN cmake -S /src -B /build -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DGGML_NATIVE=OFF -DGGML_AVX=OFF -DGGML_AVX2=OFF -DGGML_FMA=OFF \
    -DGGML_F16C=OFF -DGGML_BMI2=OFF -DGGML_CUDA=OFF \
    -DLLAMA_OPENSSL=OFF -DLLAMA_CURL=OFF -DLLAMA_BUILD_TESTS=OFF \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON \
    && cmake --build /build --target llama-server -j 4

FROM ${PYTHON_IMAGE} AS model
RUN apk add --no-cache libstdc++ libgomp
COPY --from=model-build /build/bin/ /opt/llama/
COPY model.gguf /opt/model/model.gguf
COPY model-pin.json /opt/model/model-pin.json
ENV LD_LIBRARY_PATH=/opt/llama
USER 65532:65532
ENTRYPOINT ["/opt/llama/llama-server"]
