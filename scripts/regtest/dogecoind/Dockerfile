# syntax=docker/dockerfile:1
#
# A real `dogecoind` running -regtest, for local/manual end-to-end testing
# (see docs/E2E-TESTING.md) or as a service in a docker-compose stack
# alongside dogecade and a real OIDC provider (e.g. Dex). This is a dev/test
# tool image, not the hardened production dogecade image at the repo root —
# it deliberately uses a normal Debian base (with a shell) rather than
# distroless, since the entrypoint needs to expand RPC credentials from
# environment variables into dogecoind's command-line flags.
#
# Fetches the official upstream release binary (statically linked against
# everything but glibc — confirmed via `ldd`) rather than building from
# source, and verifies its SHA256 against the checksums Dogecoin Core
# publishes with each release
# (https://github.com/dogecoin/dogecoin/releases), so the image doesn't
# depend on trusting GitHub's TLS alone.

FROM debian:bookworm-slim AS fetch
ARG DOGECOIN_VERSION=1.14.9
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /tmp

# Map Docker's TARGETARCH to the release tarball's arch suffix, then
# download and verify against the known-good SHA256 for that release
# (from https://github.com/dogecoin/dogecoin/releases/download/v${DOGECOIN_VERSION}/SHA256SUMS.asc).
# Bump both the version and these hashes together when upgrading.
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) \
        arch=x86_64-linux-gnu; \
        sha256=4f227117b411a7c98622c970986e27bcfc3f547a72bef65e7d9e82989175d4f8 ;; \
      arm64) \
        arch=aarch64-linux-gnu; \
        sha256=6928c895a20d0bcb6d5c7dcec753d35c884a471aaf8ad4242a89a96acb4f2985 ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    tarball="dogecoin-${DOGECOIN_VERSION}-${arch}.tar.gz"; \
    curl -fsSLO "https://github.com/dogecoin/dogecoin/releases/download/v${DOGECOIN_VERSION}/${tarball}"; \
    echo "${sha256}  ${tarball}" | sha256sum -c -; \
    tar xzf "${tarball}"; \
    mkdir -p /out; \
    cp "dogecoin-${DOGECOIN_VERSION}/bin/dogecoind" "dogecoin-${DOGECOIN_VERSION}/bin/dogecoin-cli" /out/

FROM debian:bookworm-slim
RUN groupadd -r dogecoin && useradd -r -g dogecoin -d /data dogecoin \
    && mkdir -p /data && chown dogecoin:dogecoin /data
COPY --from=fetch /out/dogecoind /out/dogecoin-cli /usr/local/bin/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

VOLUME ["/data"]
# regtest defaults per Dogecoin Core's src/chainparamsbase.cpp: 18444 (P2P),
# 18332 (RPC). 28332 is entrypoint.sh's own default ZMQ port (regtest has
# no upstream default). Override any of these via DOGECOIN_RPC_PORT /
# DOGECOIN_P2P_PORT / DOGECOIN_ZMQ_PORT env vars.
EXPOSE 18444 18332 28332
USER dogecoin:dogecoin
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
