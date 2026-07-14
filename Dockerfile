# syntax=docker/dockerfile:1

FROM golang:1.25 AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/dogecade ./cmd/dogecade
# Placeholder for /data, below, with the ownership a fresh named volume
# should be initialized with (distroless nonroot's UID/GID is 65532).
RUN mkdir -p /data-template && chown 65532:65532 /data-template

# UPX roughly triples the compression on top of -s -w (~14MB -> ~5MB),
# which keeps the final image comfortably under 10MB. Tradeoff: some
# antivirus/security scanners flag UPX-packed executables on heuristics
# (packing is a common malware-evasion technique), and there's a small
# in-memory decompression cost at process start. Drop this stage and
# COPY --from=build instead below if that tradeoff isn't worth it for you.
FROM build AS compress
RUN apt-get update && apt-get install -y --no-install-recommends upx-ucl \
    && rm -rf /var/lib/apt/lists/*
RUN upx --best --lzma -o /out/dogecade-upx /out/dogecade

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=compress /out/dogecade-upx /usr/local/bin/dogecade
# distroless has no shell, so /data can't be mkdir/chown'd here directly.
# Docker initializes a *new* named volume's ownership from whatever's at
# its mount point in the image at the time it's first attached — since
# nothing created /data before VOLUME previously, that was root:root, and
# the nonroot user below couldn't write to it (SQLite: "unable to open
# database file"). --chown=nonroot:nonroot on an empty COPY creates it
# instead with the right ownership baked in. (An existing named volume
# already populated as root:root won't be fixed by this — remove it,
# e.g. `docker volume rm`/`docker compose down -v`, and let it get
# recreated.)
COPY --from=build --chown=nonroot:nonroot /data-template /data
VOLUME ["/data"]
ENV DOGECADE_DB_PATH=/data/dogecade.db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dogecade"]
CMD ["serve"]
