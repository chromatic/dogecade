# Dogecade

A Go service that turns Dogecoin payments into arcade cabinet/pinball
credits, delivered to real hardware over ESP8266/Tasmota relay boards.
Customers buy a token balance (or pay a machine directly), redeem tokens for
credits, and dogecade fires the machine's relay over HTTP. It's
receive-only: no spend keys live in the deployment, and the operator sweeps
funds with their own wallet.

See `docs/design.md` for the full architecture and non-goals, and
`docs/plan.md` for the phased build plan this was implemented against.

## Quick start: local end-to-end stack

The fastest way to see the whole system running: a real `dogecoind
-regtest` node, a real OIDC provider, and dogecade itself, wired together
and pre-seeded with an admin account, deposit addresses, a demo machine,
and a relay binding.

```bash
docker compose -f scripts/regtest/docker-compose.yml up --build
```

Then visit `http://localhost:8080/auth/login` and sign in as
`admin@dogecade.local` / `admin` (or `customer@dogecade.local` /
`customer` for the ordinary buy/redeem flow). Full walkthrough, including
how to pay a regtest address, mine confirmations, and watch a relay board
fire, is in `docs/E2E-TESTING.md`.

## Build and run

```
docker build -t dogecade:latest .
docker run -e DOGECADE_DB_PATH=/data/dogecade.db \
           -e DOGECADE_BASE_URL=https://arcade.example.com \
           -v dogecade-data:/data -p 8080:8080 dogecade:latest
```

Or via Compose. See `docker-compose.yml` for the full environment variable
list and `network_mode: host` vs bridge networking guidance (relay boards
and `dogecoind` usually live on the LAN, which affects which mode you
want).

Building from source instead of a container:

```
go build -o dogecade ./cmd/dogecade
./dogecade serve
```

`dogecade` is a single static binary with subcommands beyond `serve`:
`addresses` (import/generate deposit addresses), `relays` (test-fire a
machine, create a relay board, bind it to a machine), `users` (seed an
admin account before first login), and `machines` (create a machine). Run
any of them with no arguments for usage, or see `cmd/dogecade/main.go`.

Full operational guidance, node requirements, address inventory loading,
backups, and the alert catalogue, is in `docs/runbook.md`.

## Testing

```
go vet ./...
go build ./...
go test ./...
golangci-lint run    # same linter CI runs; install via https://golangci-lint.run
```

That's fast, in-process unit testing against `internal/store` (in-memory
SQLite) and fakes for the Dogecoin node and Tasmota boards, no external
processes required. Two more layers exist for testing against the real
thing:

- `go test -tags integration ./internal/chain/corerpc/...`, narrow tests
  against a real `dogecoind -regtest` binary on `PATH`.
- `perl scripts/regtest/e2e.pl`, an automated pass through the *entire*
  system (real dogecade binary, real OIDC login, a real regtest payment, a
  fake Tasmota board) over HTTP, the same way Dogecoin Core's own
  functional-test framework drives a live `dogecoind`.

See `docs/TESTING.md` for what each layer needs and when to reach for it,
and `docs/E2E-TESTING.md` if you want to drive the system yourself in a
browser instead of watching a script do it.

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
executables on heuristics) if you'd rather skip it: without it, the image
lands closer to 16 MB.

## Documentation map

- `docs/design.md`, architecture, goals/non-goals, data model.
- `docs/plan.md`, phased implementation plan (what's built, what's next).
- `docs/runbook.md`, operating a live deployment: node requirements,
  address inventory, backups, alerts.
- `docs/TESTING.md`, the three testing layers and what each needs.
- `docs/E2E-TESTING.md`, driving a full local stack by hand in a browser.
