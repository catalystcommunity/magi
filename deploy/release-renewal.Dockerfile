FROM node:26.1.0-bookworm-slim@sha256:8c676dbdbfc322815df09c910e80685fc5de08baa21c3b6189627e302b1bf131
WORKDIR /app
COPY --chown=65532:65532 app/ /app/
USER 65532:65532
ENTRYPOINT ["node", "release/renewal-run.mjs"]
