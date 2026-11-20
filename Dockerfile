# syntax=docker/dockerfile:1
ARG BACKEND=sim

FROM rust:1-bookworm AS media
ARG BACKEND
RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY .cargo .cargo
COPY crates crates
COPY engine engine
COPY scripts/setup-sussurro.sh scripts/
RUN if [ "$BACKEND" = sussurro ]; then scripts/setup-sussurro.sh sherpa; fi
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    set -eu; \
    features=""; [ "$BACKEND" = sussurro ] && features="--features sussurro"; \
    cargo build --release --locked -p ambivox-media $features; \
    mkdir -p /out/bin /out/lib; \
    cp target/release/ambivox-media /out/bin/; \
    if [ "$BACKEND" = sussurro ]; then \
      cp -L "$(find target/release/build -path '*/lib/libambivox_engine.so' | head -n 1)" /out/lib/; \
      cp -L third_party/sherpa-onnx/lib/*.so /out/lib/; \
    fi

FROM golang:1-bookworm AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/bin/ambivox ./cmd/ambivox

FROM debian:bookworm-slim
ARG BACKEND
RUN if [ "$BACKEND" = sussurro ]; then \
      apt-get update && apt-get install -y --no-install-recommends libgomp1 && rm -rf /var/lib/apt/lists/*; \
    fi \
 && useradd --system --uid 10001 --home-dir /data ambivox \
 && mkdir -p /data /run/ambivox /models \
 && chown ambivox /data /run/ambivox
COPY --from=media /out/lib/ /usr/local/lib/ambivox/
COPY --from=media /out/bin/ /usr/local/bin/
COPY --from=server /out/bin/ /usr/local/bin/
ENV LD_LIBRARY_PATH=/usr/local/lib/ambivox \
    AMBIVOX_LISTEN=:8443 \
    AMBIVOX_MEDIA_SOCKET=/run/ambivox/media.sock \
    AMBIVOX_DATA_DIR=/data \
    AMBIVOX_TLS_SELF_SIGNED=1 \
    AMBIVOX_UDP_PORT=8443
USER ambivox
EXPOSE 8443/tcp 8443/udp
CMD ["ambivox"]
