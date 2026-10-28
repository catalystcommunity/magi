# Default development environment for Magi agents.
#   docker build -t magi-dev-env primitives/execution/images/dev
# Login shells (bash -lc) read /etc/profile, which resets PATH; profile.d/go.sh
# puts Go back. envd is not in this image. The execution service delivers it at start.
FROM golang:1.26-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      python3 python3-venv python3-pip ripgrep git curl make jq ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 1000 --user-group --create-home --home-dir /home/magi --shell /bin/bash magi \
 && mkdir -p /work && chown magi:magi /work \
 && mkdir -p /home/magi/go /home/magi/.cache && chown -R magi:magi /home/magi \
 && printf 'export PATH=/home/magi/go/bin:/usr/local/go/bin:$PATH\n' > /etc/profile.d/go.sh
ENV HOME=/home/magi GOPATH=/home/magi/go GOCACHE=/home/magi/.cache/go-build \
    PATH=/home/magi/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
USER 1000:1000
WORKDIR /work
CMD ["sleep", "infinity"]
