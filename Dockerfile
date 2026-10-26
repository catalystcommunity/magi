FROM scratch
COPY --chown=65532:65532 dist/magi /magi
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/magi"]
