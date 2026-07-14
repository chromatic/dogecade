# Manual End-to-End Testing on a Local Emulated Network

This is a companion to `docs/TESTING.md`, for a different use case: running a
persistent `dogecade` instance by hand against a real `dogecoind -regtest`
node and a **real** OIDC provider (not the mock one `scripts/regtest/e2e.pl`
uses), so you can click through the admin console and customer flow
yourself, adding machines, importing addresses, watching a payment land,
and firing a relay (fake or real hardware), rather than watching a script
do it.

Use `scripts/regtest/e2e.pl` (see `docs/TESTING.md`) when you want an
automated, scripted pass through the whole system. Use this guide when you
want to drive it yourself in a browser.

## Fast path: docker compose

`scripts/regtest/docker-compose.yml` assembles a full click-through stack
from five services. This section explains what each one does and how to
drive it, so you can use it as both a startup guide and a reference while
testing. If you'd rather assemble the pieces by hand (to swap in Google
instead of Dex, run dogecade natively, or just see what the compose file
is automating), skip to "1. A regtest Dogecoin node" below.

### What's in the stack

- **`dogecoind-regtest`**: a real `dogecoind -regtest` node (Option B
  below), the source of truth for balances and confirmations.
- **`dogecade-seed-admins`, `dogecade-seed-addresses`,
  `dogecade-seed-machine`, `dogecade-seed-relay-board`,
  `dogecade-seed-relay-bind`**: five one-shot setup steps that make the
  stack usable the moment it comes up, instead of greeting you with "out
  of deposit addresses", no admin access, or "no active relay binding" on
  your first test-fire.
- **`dex`**: a real, self-hosted OIDC provider with two fixed test logins
  (admin and customer), standing in for Google in production.
- **`dogecade`**: the application itself, serving the admin console and
  customer buy/redeem flow.

You'll also want **`scripts/regtest/fake_relay.pl`** running in another
terminal (see "The seed services" below) so the relay board the stack
seeds has something real to dispatch to.

Bring the whole thing up with:

```bash
docker compose -f scripts/regtest/docker-compose.yml up --build
```

The sections below walk through what happens as each service starts, in
the order they start in.

### The regtest node

`dogecoind-regtest`'s entrypoint
(`scripts/regtest/dogecoind/entrypoint.sh`) bootstraps the chain itself on
startup: it creates the wallet (tolerating `createwallet`'s `-32601 Method
not found` on builds that auto-load a single default wallet instead of
registering that RPC), then mines up to 101 blocks if the chain doesn't
already have that many. That gets it past `initialblockdownload` with
spendable coinbase output, with no manual step needed. It's idempotent: on
a restart against an already-bootstrapped `/data` volume, it just checks
the block count and does nothing.

Watch it come up with:

```bash
docker compose -f scripts/regtest/docker-compose.yml logs dogecoind-regtest
```

Look for `entrypoint: chain bootstrapped` (or `already has N blocks,
skipping bootstrap` on a re-run) to confirm it ran.

The compose file also gives it a healthcheck: `dogecade-seed-admins` and
the other seed services wait on `condition: service_healthy` before
running, since they need a loaded wallet and a mined chain to call
`getnewaddress`/import against. That check has to be read-only
(`getblockchaininfo`, not `getnewaddress`), because Docker keeps
re-running a healthcheck for the whole life of the container, not just
until it first passes. An earlier version of this check called
`getnewaddress`, and quietly grew the wallet's keypool by one key every
few seconds, forever.

If you ever need to mine more manually (to push a payment's confirmations
past `min_confirmations`, or after topping up `/buy` deposit addresses,
see "Mock data" below), the same `dogecoin-cli` commands from Option B
further down work against this compose service too. Just swap
`docker exec dogecoind-regtest` for
`docker compose -f scripts/regtest/docker-compose.yml exec dogecoind-regtest`.

### The seed services

Once `dogecoind-regtest` reports healthy, five one-shot services run in
sequence, each waiting on the previous one's
`condition: service_completed_successfully`:

1. `dogecade-seed-admins` runs `dogecade users seed-admins`, which creates
   a user row with `is_admin=1` for every issuer/subject pair in
   `DOGECADE_ADMIN_SUBJECTS`, ahead of that user's first login. This
   sidesteps a real limitation: `GetOrCreateBySubjectHash`
   (`internal/services/users.go`) only sets `is_admin` at the moment a
   user row is first *created*, so without this step Dex's fixed admin
   login would sign in as an ordinary customer.
2. `dogecade-seed-addresses` runs
   `dogecade addresses generate --count=10 --purpose=token_deposit`,
   generating 10 fresh regtest addresses from the node and importing them
   as the deposit pool `/buy` draws from.
3. `dogecade-seed-machine` runs
   `dogecade machines create cabinet-1 "Demo Cabinet"`, creating one demo
   machine so `/admin/machines` isn't empty on first login.
4. `dogecade-seed-relay-board` runs
   `dogecade relays create-board fake-relay http://127.0.0.1:9001`,
   registering a relay board pointed at the port `fake_relay.pl` listens
   on by default. `127.0.0.1:9001` reaches your host directly because
   this service, like the rest of the stack, uses `network_mode: host`.
   Creating the board record doesn't require anything to actually be
   listening on that port yet.
5. `dogecade-seed-relay-bind` runs
   `dogecade relays bind cabinet-1 fake-relay 1`, binding that board's
   relay channel 1 to the demo machine, so a fresh stack can test-fire or
   redeem successfully without a trip through `/admin/machines` first.

Each step is a thin wrapper around a general-purpose `dogecade` CLI
subcommand (see `cmd/dogecade/main.go`), not e2e-only glue: all five are
just as useful against a live deployment, for example to top up its
deposit pool or script a new machine's relay wiring. Each is also safe to
re-run against an already-seeded volume.

Confirm they ran with:

```bash
docker compose -f scripts/regtest/docker-compose.yml logs dogecade-seed-admins dogecade-seed-addresses dogecade-seed-machine dogecade-seed-relay-board dogecade-seed-relay-bind
```

The board record existing doesn't mean dispatch will succeed yet: start
`fake_relay.pl` on port 9001 (see "6. Mock Tasmota" below) before
test-firing or redeeming, or you'll see connection-refused errors instead
of "no active relay binding".

### Signing in

With the node bootstrapped and the seed steps done, `dex` and `dogecade`
start. Visit `http://localhost:8080/auth/login` and sign in with Dex's
fixed credentials.

Dex's login form asks for **Email Address**, not username. The `username`
field in `config.yaml` only becomes the `preferred_username` claim; it's
not the login identifier. Log in with **`admin@dogecade.local` / `admin`**
for the admin console (pre-authorized via `DOGECADE_ADMIN_SUBJECTS` in the
compose file and the `dogecade-seed-admins` step above, no DB-flip dance
needed, unlike the Google path in step 4 below), or
**`customer@dogecade.local` / `customer`** for the ordinary buy/redeem
flow.

From here, everything from "5. Mock data" onward (paying an address,
wiring a relay board) applies unchanged, except the mock data is already
seeded for you.

## 1. A regtest Dogecoin node

Two ways to get one, pick whichever fits how you're running the rest of
the stack.

### Option A: native binary

A persistent instance you keep running and drive by hand:

```bash
mkdir -p ~/dogecade-regtest
dogecoind -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme -rpcport=22555 -daemon=1

dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme createwallet regtest || true

MINE_ADDR=$(dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme getnewaddress)
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme generatetoaddress 101 "$MINE_ADDR"
```

(`createwallet` may report `error code: -32601 / Method not found` on builds
that auto-load a single default `wallet.dat` at startup instead of
registering that RPC. Harmless: `getnewaddress`/`generatetoaddress` don't
depend on it.)

### Option B: Docker

`scripts/regtest/dogecoind/` has a Dockerfile that fetches the official
upstream `dogecoind`/`dogecoin-cli` release binaries (checksum-verified
against Dogecoin Core's published `SHA256SUMS`, not just trusted off
GitHub), wraps them in a small entrypoint that expands RPC credentials from
environment variables, and runs `-regtest`. Useful if you're assembling a
docker-compose stack (dogecade + this + Dex) rather than running things
natively.

```bash
docker build -t dogecade-regtest-node \
  -f scripts/regtest/dogecoind/Dockerfile scripts/regtest/dogecoind

docker volume create dogecade-regtest-data
docker run -d --name dogecoind-regtest \
  -e DOGECOIN_RPC_USER=dogecade -e DOGECOIN_RPC_PASSWORD=changeme \
  -p 18332:18332 -p 18444:18444 -p 28332:28332 \
  -v dogecade-regtest-data:/data \
  dogecade-regtest-node

docker exec dogecoind-regtest dogecoin-cli -regtest \
  -rpcuser=dogecade -rpcpassword=changeme createwallet regtest || true
MINE_ADDR=$(docker exec dogecoind-regtest dogecoin-cli -regtest \
  -rpcuser=dogecade -rpcpassword=changeme getnewaddress)
docker exec dogecoind-regtest dogecoin-cli -regtest \
  -rpcuser=dogecade -rpcpassword=changeme generatetoaddress 101 "$MINE_ADDR"
```

`scripts/regtest/docker-compose.yml` (see "Fast path" above) runs every
service with `network_mode: host` rather than a bridge network, so inside
that compose file, `dogecade`'s `DOGECOIND_RPC_URL` is
`http://127.0.0.1:18332`, same as running natively, not a
`dogecoind-regtest` service-name hostname. That's also why Dex's issuer
URL there is a plain `http://localhost:5556/dex` reachable from both your
browser and dogecade's server-side calls. See that compose file's header
comment for why host networking specifically avoids a hostname
split-brain between the two.

`DOGECOIN_RPC_USER`/`DOGECOIN_RPC_PASSWORD` default to `regtest`/`regtest`
if unset; `DOGECOIN_RPC_PORT`/`DOGECOIN_P2P_PORT`/`DOGECOIN_ZMQ_PORT`
default to the regtest standard ports (`18332`/`18444`) plus `28332` for
ZMQ (regtest has no upstream-standard ZMQ port). Override the Dogecoin
Core version with `--build-arg DOGECOIN_VERSION=x.y.z`, bump the SHA256
pins in the Dockerfile alongside it (from that release's
`SHA256SUMS.asc` on the
[releases page](https://github.com/dogecoin/dogecoin/releases)).

---

Either way, 101 blocks because regtest requires 100 confirmations before
coinbase output is spendable, `$MINE_ADDR` now holds spendable regtest
DOGE you can send to any address the admin console assigns.

Keep `dogecoin-cli ... generatetoaddress N "$MINE_ADDR"` handy, regtest
blocks don't mine themselves, and dogecade's deposit pipeline needs
confirmations (`min_confirmations`, default 1) before it credits a payment.

### A note on regtest address prefixes

Real `dogecoind -regtest` addresses start with `m`/`n` (Bitcoin's testnet
`PUBKEY_ADDRESS` prefix, `0x6f`) rather than Dogecoin's own dedicated
testnet prefix (`0x71`). This is genuine upstream behavior, in Dogecoin
Core's `src/chainparams.cpp`, `CTestNetParams` and `CRegTestParams` are
separate classes with separate `base58Prefixes[PUBKEY_ADDRESS]` values
(`0x71` for testnet, `0x6f` for regtest), not a quirk of a particular
build. `internal/keyring/keyring.go`'s `ValidateAddress` accepts both, so
address import works against a real regtest node.

## 2. A real OIDC provider

Both customer sign-in and the admin console require OIDC login, there's
no bypass. Two practical options for local testing:

**Google** (simplest if you already have a Google account; works against
plain `http://localhost`, no TLS needed for local testing):

1. Google Cloud Console → APIs & Services → Credentials → Create OAuth
   client ID → Web application.
2. Authorized redirect URI: `http://localhost:8080/auth/callback`.
3. Note the client ID and secret.

**Dex** ([dexidp/dex](https://github.com/dexidp/dex)), if you'd rather not
touch a Google account: a real, self-hosted OIDC server (run via Docker)
with static test users. Its `sub` claim is stable across restarts, since
the same static users are re-declared identically every container start,
but it's not the `userID` string from `config.yaml` verbatim. Dex's
`local` connector base64-encodes a small protobuf of `{user_id, conn_id}`
into `sub` instead, so treat it as opaque: sign in once, read the actual
value off the `oidc login` log line dogecade emits at
`internal/auth/handlers.go`, and use that in `DOGECADE_ADMIN_SUBJECTS`
before subsequent logins. It's still one login ahead of the Google path
below, which needs a DB edit after every fresh account.

## 3. Run dogecade

```bash
go build -o dogecade ./cmd/dogecade

DOGECADE_DB_PATH=~/dogecade-regtest/dogecade.db \
DOGECADE_BASE_URL=http://localhost:8080 \
DOGECADE_LISTEN_ADDR=:8080 \
DOGECOIND_RPC_URL=http://127.0.0.1:22555 \
DOGECOIND_RPC_USER=dogecade \
DOGECOIND_RPC_PASS=changeme \
DOGECADE_OIDC_ISSUER_URL=https://accounts.google.com \
DOGECADE_OIDC_CLIENT_ID=<your client id> \
DOGECADE_OIDC_CLIENT_SECRET=<your client secret> \
./dogecade serve
```

Leave `DOGECADE_ADMIN_SUBJECTS` unset for now (Google), see step 4. If
using Dex with a known `sub`, set it here as `<issuer>|<subject>` and skip
step 4 entirely.

## 4. Become admin (Google / any provider where you don't control `sub`)

`GetOrCreateBySubjectHash` (`internal/services/users.go`) only sets
`is_admin` at the moment a user row is first created, based on whether
`issuer|subject` matched `DOGECADE_ADMIN_SUBJECTS` *at that login*. Since
you don't know your own Google `sub` in advance, the practical path is:

1. Visit `http://localhost:8080/auth/login` and sign in with your real
   account. This creates your user row as non-admin.
2. Flip it directly in the DB:
   ```bash
   sqlite3 ~/dogecade-regtest/dogecade.db \
     "UPDATE users SET is_admin=1 WHERE id=(SELECT id FROM users ORDER BY id DESC LIMIT 1);"
   ```
3. Log out and back in. Sessions bake in `is_admin` from the DB row at
   `session.Issue()` time (`internal/auth/session.go`), so a fresh session
   after the DB edit picks up admin access immediately, no restart needed.

## 5. Mock data: addresses and machines

If you're using the "Fast path" compose stack above, this already
happened automatically (`dogecade-seed-admins`/`-addresses`/`-machine`).
You only need this section if you're running dogecade natively, want to
top up the deposit pool beyond the initial 10 addresses, or want another
machine.

The `dogecade` binary can generate addresses from the node and import them
itself, in one step:

```bash
./dogecade addresses generate --count=10 --purpose=token_deposit
```

That's equivalent to generating a batch by hand and importing it:

```bash
for i in $(seq 1 10); do
  dogecoin-cli -regtest -datadir=~/dogecade-regtest \
    -rpcuser=dogecade -rpcpassword=changeme getnewaddress
done > batch.txt

./dogecade addresses import --purpose=token_deposit batch.txt
```

Or paste addresses into `/admin/addresses` in the browser (same effect,
plus you can watch the pool count update).

Create a machine from the CLI:

```bash
./dogecade machines create cabinet-1 "Demo Cabinet"
```

Or use the "Add machine" form at `/admin/machines`.

Then in `/admin/machines`: create a machine, create a relay board (see
below for the base URL), bind the machine to a relay channel. On the
customer side, `/buy` assigns one of the imported addresses; pay it and
mine confirmations to see it credited.

If you're on the "Fast path" compose stack, run these against the
`dogecoind-regtest` container rather than a native `dogecoind`, no
`-datadir` needed, since `docker compose exec` runs inside the container
against its own `/data`:

```bash
docker compose -f scripts/regtest/docker-compose.yml exec dogecoind-regtest \
  dogecoin-cli -regtest -rpcuser=dogecade -rpcpassword=changeme \
  sendtoaddress <assigned address> 1

MINE_ADDR=$(docker compose -f scripts/regtest/docker-compose.yml exec dogecoind-regtest \
  dogecoin-cli -regtest -rpcuser=dogecade -rpcpassword=changeme getnewaddress)

docker compose -f scripts/regtest/docker-compose.yml exec dogecoind-regtest \
  dogecoin-cli -regtest -rpcuser=dogecade -rpcpassword=changeme \
  generatetoaddress 2 "$MINE_ADDR"
```

If you're on Option A/B (native binary or a standalone Docker `dogecoind`),
use the same commands as elsewhere in this doc:

```bash
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme sendtoaddress <assigned address> 1
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme generatetoaddress 2 "$MINE_ADDR"
```

Refresh `/buy` (or watch the SSE-driven status text), it should flip to
credited within a few seconds of the chain watcher's next poll. Bump the
`generatetoaddress` count if your `min_confirmations` setting is higher
than 2.

## 6. Mock Tasmota (or your real arcade)

If you're on the "Fast path" compose stack, `dogecade-seed-relay-board`
and `dogecade-seed-relay-bind` already registered a `fake-relay` board
pointed at `http://127.0.0.1:9001` and bound it to `cabinet-1`, so you
only need to start `fake_relay.pl` itself (below) and you're ready to
test-fire or redeem. The rest of this section is for the manual,
piece-by-piece setup, or if you want a second board/machine.

A machine with no relay board bound to it isn't a bug you'll hit
elsewhere, it's the default state after "Mock data" above if you're
running dogecade natively, since `machines create` only creates the
machine row, not a relay board or binding. Redeeming against an unbound
machine fails dispatch and refunds the redemption; you'll see something
like this in your token history:

```
relay dispatch failed after 5 attempts: no active relay binding for machine 1
```

That's expected until you wire up a board, real or fake, below.

`scripts/regtest/fake_relay.pl` is standalone and reusable outside the
scripted harness (it's **not** started by
`scripts/regtest/docker-compose.yml`, start it yourself, even on the Fast
path). It implements enough of Tasmota's `/cm?cmnd=...` console interface
for `internal/relay/client.go` to drive, and logs every command it
receives:

```bash
perl scripts/regtest/fake_relay.pl 9001 /tmp/relay.log
```

If you're running dogecade natively rather than through the Fast path
compose stack, create the relay board and binding yourself instead of
relying on the seed steps, either from the CLI:

```bash
./dogecade relays create-board fake-relay http://127.0.0.1:9001
./dogecade relays bind cabinet-1 fake-relay 1
```

or in `/admin/relays`/`/admin/machines`: create a relay board pointing at
its base URL (use whatever address is actually reachable from wherever
dogecade is running, e.g. `http://host.docker.internal:9001` for a
bridge-networked container instead of `127.0.0.1`), then bind that
board's channel to your machine.

Every command hitting the board (the admin console's "test fire" button,
and real redemption pulses from a customer's `/m/{slug}` redemption) gets
appended as a JSON line to `/tmp/relay.log`, so you can confirm dispatch is
actually firing without touching hardware:

```bash
tail -f /tmp/relay.log
```

You can also check the admin dashboard at `http://localhost:8080/admin`.
Its "recent redemptions" panel reads `credit_pulses.state`; look for
`state = sent`. Note that `docker compose logs dogecade` (or
`dogecade`'s own stdout) only logs dispatch *failures*
(`internal/relay/dispatcher.go`'s `slog.Error` calls). There's no
success log line, so silence there doesn't confirm anything either way;
use the relay log or the dashboard instead.

When you're ready to test against real hardware, point the board's base
URL at your arcade's actual Tasmota IP instead (e.g.
`http://192.168.1.50`), nothing else in the flow changes; `dogecade`
doesn't know or care whether it's talking to a real board or the fake one.

## Cleanup

```bash
dogecoin-cli -regtest -datadir=~/dogecade-regtest -rpcuser=dogecade -rpcpassword=changeme stop
rm -rf ~/dogecade-regtest
```
