# Testing Dogecade

Three layers, from fastest/narrowest to slowest/most realistic:

1. Unit tests (`go test ./...`) — no external processes, run these constantly.
2. Go regtest integration tests (`-tags integration`) — spin up a real
   `dogecoind -regtest` per test, but only exercise narrow slices (a single
   RPC call, a single service method).
3. The Perl end-to-end harness (`scripts/regtest/e2e.pl`) — spins up a real
   `dogecoind -regtest`, a real `dogecade` server binary, a mock OIDC
   provider, and a fake Tasmota relay board, then drives the *entire*
   payment → credit → redeem → relay-fire pipeline over HTTP exactly as a
   browser would. Use this when you want to manually watch (or automate) the
   whole system working end to end, the same way Dogecoin Core's own
   Python functional-test framework drives a live `dogecoind`.

For driving the system yourself in a browser — real OIDC login, mock (or
real) machine/address data, a mock (or real) Tasmota board — instead of
watching a script do it, see `docs/E2E-TESTING.md`.

## Building

```
go build ./...                      # compile everything
go build -o dogecade ./cmd/dogecade # just the server binary
```

`dogecade` is a single static binary with subcommands: `version`, `serve`,
`addresses import`, `relays`. See `README.md` for the container build.

## Unit tests

```
go vet ./...
go build ./...
go test ./...
golangci-lint run    # same linter CI runs; install via https://golangci-lint.run
```

These use `internal/store` against an in-memory/temp-file SQLite DB and fakes
for the Dogecoin node and Tasmota boards — no external processes required.

## Go regtest integration tests

`internal/chain/corerpc/regtest.go` and `regtest_integration_test.go` are
gated behind the `integration` build tag and need a real `dogecoind` binary
on `PATH` (or they `t.Skip()`):

```
go test -tags integration ./internal/chain/corerpc/...
```

`StartRegtestNode(t)` launches `dogecoind -regtest` in a temp data dir,
waits for RPC readiness, creates a wallet, and mines 101 blocks (regtest
needs 100 confirmations before coinbase output is spendable) — then hands
back a ready `*Client`. These tests are narrow: one RPC call or one service
method against a live node, not the full application.

## Manual end-to-end regtest harness

`scripts/regtest/e2e.pl` drives the *whole* system — real `dogecade` HTTP
server, real OIDC login flow (against a mock provider so you don't need a
live Google/Okta issuer), a real regtest Dogecoin payment, and a fake
Tasmota relay board — through the actual customer/admin HTTP surface.

### Requirements

- A `dogecoind` binary (real Dogecoin Core, not a mock) — set `DOGECOIND_BIN`
  if it's not on `PATH`.
- Go toolchain (the harness builds `dogecade` fresh from source each run).
- Perl with `Mojolicious`, `LWP::UserAgent`, `Crypt::PK::RSA`,
  `HTTP::Cookies`, `JSON::PP` (all common distro/cpan packages — nothing
  Dogecoin-specific).

### Running it

```
export DOGECOIND_BIN=/path/to/dogecoind   # if not on PATH
perl scripts/regtest/e2e.pl
```

On success it prints `ALL CHECKS PASSED` and cleans up every process it
started (dogecoind, dogecade, the mock OIDC provider, the fake relay) plus
its temp workspace. On failure it prints `FAIL: ...` and exits non-zero,
same cleanup either way.

Set `E2E_KEEP_WORKSPACE=1` to keep the temp workspace (dogecoind data dir,
dogecade SQLite DB, and every component's log file) around after the run
for debugging — the harness prints the workspace path on the first line.

### What it does, step by step

1. Builds `dogecade` from the repo at `HEAD` (`go build ./cmd/dogecade`).
2. Starts `dogecoind -regtest` in a fresh temp data dir, waits for RPC,
   mines 101 blocks so there's spendable coinbase output.
3. Starts `scripts/regtest/mock_oidc.pl` — a minimal OIDC provider
   (discovery doc, JWKS, `/authorize`, `/token`) that signs real RS256 ID
   tokens with a throwaway RSA key generated at startup. `/authorize`
   auto-approves whichever identity is passed via `login_hint` (no login UI
   to click through), so the harness can log in as different
   customer/admin subjects without a browser.
4. Starts `scripts/regtest/fake_relay.pl` — a fake Tasmota board that
   answers `/cm?cmnd=...` like a real board and logs every command it
   receives to a file, so the harness can assert a relay pulse actually
   fired.
5. Starts the `dogecade` binary (`serve`) pointed at the regtest node and
   the mock OIDC provider, with a fresh SQLite DB.
6. As an admin (real OIDC login flow via the mock provider): imports a
   regtest deposit address, creates a machine, registers the fake relay
   board, and binds the machine to a relay channel.
7. As a customer (separate OIDC identity/session): starts a purchase to get
   a deposit address, then the harness sends a **real regtest Dogecoin
   payment** to it via `dogecoind`'s RPC and mines confirming blocks.
8. Polls `/buy/status` until the deposit shows as credited (proving the
   chain watcher + deposit pipeline detected and processed the payment).
9. Redeems a token at the machine over HTTP, then checks the fake relay's
   log for the `Power1 ON` pulse (proving the redemption → dispatcher →
   Tasmota HTTP call chain fired).

### Extending it

The three scripts are independent and can be reused for other manual
exploration:

- `perl scripts/regtest/mock_oidc.pl <port> <keyfile>` on its own gives you
  an OIDC provider to point a manually-run `dogecade serve` at — set
  `DOGECADE_OIDC_ISSUER_URL`/`_CLIENT_ID`/`_CLIENT_SECRET` and hit
  `/auth/login?redirect=/` with `curl` or a browser, adding
  `&login_hint=<subject>` to the provider's `/authorize` URL to pick who
  you're signed in as.
- `perl scripts/regtest/fake_relay.pl <port> <logfile>` gives you a Tasmota
  stand-in for admin-console "test fire" clicks without real hardware.
- The main script's helper subs (`doge_rpc`, `oidc_login`, `form_post`) are
  ordinary Perl and can be copy-pasted into a REPL-style one-off script for
  ad hoc exploration of a specific flow (e.g. direct-pay, address rotation).

### Known issue surfaced by this harness

`internal/keyring/keyring.go`'s `ValidateAddress` originally only accepted
`0x71` as the testnet/regtest P2PKH version byte. Real `dogecoind -regtest`
(Dogecoin Core, confirmed via `dogecoind --version`) actually produces
addresses with version byte `0x6f` (Bitcoin's testnet byte, reused by
Dogecoin's regtest chainparams) — so address import silently rejected every
real regtest address until `0x6f` was added to the accepted set. If you hit
`"invalid address"` errors importing addresses from a real node again,
check this whitelist first.
