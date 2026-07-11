# Dogecade

A Go service that turns Dogecoin payments into arcade cabinet/pinball
credits, delivered to real hardware over ESP8266/Tasmota relay boards. See
`docs/design.md` for the architecture and `docs/plan.md` for build phases.

## Build and run

```
docker build -t dogecade:latest .
docker run -e DOGECADE_DB_PATH=/data/dogecade.db \
           -e DOGECADE_BASE_URL=https://arcade.example.com \
           -v dogecade-data:/data -p 8080:8080 dogecade:latest
```

Or via Compose — see `docker-compose.yml` for the full environment variable
list and `network_mode: host` vs bridge networking guidance (relay boards
and `dogecoind` usually live on the LAN, which affects which mode you
want).

Full operational guidance — node requirements, address inventory loading,
backups, and the alert catalogue — is in `docs/runbook.md`.

## Image size

The final image is `gcr.io/distroless/static-debian12:nonroot` (bundles CA
certificates and tzdata, no shell/package manager) plus a single static Go
binary.

**`docker image list` reports 6.79 MB** for `dogecade:latest` built from
this Dockerfile (UPX-packed binary + distroless base), comfortably under
the 10 MB target.

That's with the Dockerfile's `compress` stage applying UPX (`--best
--lzma`) to the stripped Go binary (`go build -trimpath
-ldflags="-s -w"`, `CGO_ENABLED=0`, pure-Go SQLite driver so no libc
dependency), which shrinks it from 14.4 MB to 4.7 MB. See the comment on
that stage for the tradeoff (some AV/security scanners flag UPX-packed
executables on heuristics) if you'd rather skip it — without it, the image
lands closer to 16 MB.
