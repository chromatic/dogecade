# Manual End-to-End Testing on a Local Emulated Network

This is a companion to `docs/TESTING.md`, for a different use case: running a
persistent `dogecade` instance by hand against a real `dogecoind -regtest`
node and a **real** OIDC provider (not the mock one `scripts/regtest/e2e.pl`
uses), so you can click through the admin console and customer flow
yourself — adding machines, importing addresses, watching a payment land,
and firing a relay (fake or real hardware) — rather than watching a script
do it.

Use `scripts/regtest/e2e.pl` (see `docs/TESTING.md`) when you want an
automated, scripted pass through the whole system. Use this guide when you
want to drive it yourself in a browser.

## 1. A regtest Dogecoin node

Same idea as the automated harness, but a persistent instance you keep
running and drive by hand:

```bash
mkdir -p ~/dogecade-regtest
dogecoind -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme -rpcport=22555 -daemon=1

dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme createwallet regtest

MINE_ADDR=$(dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme getnewaddress)
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme generatetoaddress 101 "$MINE_ADDR"
```

101 blocks because regtest requires 100 confirmations before coinbase
output is spendable — `$MINE_ADDR` now holds spendable regtest DOGE you can
send to any address the admin console assigns.

Keep `dogecoin-cli ... generatetoaddress N "$MINE_ADDR"` handy — regtest
blocks don't mine themselves, and dogecade's deposit pipeline needs
confirmations (`min_confirmations`, default 1) before it credits a payment.

### A note on regtest address prefixes

Real `dogecoind -regtest` addresses start with `m`/`n` (Bitcoin's testnet
`PUBKEY_ADDRESS` prefix, `0x6f`) rather than Dogecoin's own dedicated
testnet prefix (`0x71`). This is genuine upstream behavior — in Dogecoin
Core's `src/chainparams.cpp`, `CTestNetParams` and `CRegTestParams` are
separate classes with separate `base58Prefixes[PUBKEY_ADDRESS]` values
(`0x71` for testnet, `0x6f` for regtest) — not a quirk of a particular
build. `internal/keyring/keyring.go`'s `ValidateAddress` accepts both, so
address import works against a real regtest node.

## 2. A real OIDC provider

Both customer sign-in and the admin console require OIDC login — there's
no bypass. Two practical options for local testing:

**Google** (simplest if you already have a Google account; works against
plain `http://localhost`, no TLS needed for local testing):

1. Google Cloud Console → APIs & Services → Credentials → Create OAuth
   client ID → Web application.
2. Authorized redirect URI: `http://localhost:8080/auth/callback`.
3. Note the client ID and secret.

**Dex** ([dexidp/dex](https://github.com/dexidp/dex)), if you'd rather not
touch a Google account: a real, self-hosted OIDC server (run via Docker)
with static test users. Unlike Google, you control the `sub` claim value up
front via Dex's config, which sidesteps the admin chicken-and-egg problem
in step 4 below — you can put the right value in `DOGECADE_ADMIN_SUBJECTS`
before your first login instead of promoting yourself after the fact.

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

Leave `DOGECADE_ADMIN_SUBJECTS` unset for now (Google) — see step 4. If
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
   after the DB edit picks up admin access immediately — no restart needed.

## 5. Mock data: addresses and machines

Generate a batch of addresses from the regtest node and import them
through the real admin UI or CLI — these are genuine regtest addresses the
node can receive payments to, just not tied to real money:

```bash
for i in $(seq 1 10); do
  dogecoin-cli -regtest -datadir=~/dogecade-regtest \
    -rpcuser=dogecade -rpcpassword=changeme getnewaddress
done > batch.txt

./dogecade addresses import --purpose=token_deposit batch.txt
```

Or paste them into `/admin/addresses` in the browser (same effect, plus
you can watch the pool count update).

Then in `/admin/machines`: create a machine, create a relay board (see
below for the base URL), bind the machine to a relay channel. On the
customer side, `/buy` assigns one of the imported addresses; pay it and
mine confirmations to see it credited:

```bash
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme sendtoaddress <assigned address> 1
dogecoin-cli -regtest -datadir=~/dogecade-regtest \
  -rpcuser=dogecade -rpcpassword=changeme generatetoaddress 2 "$MINE_ADDR"
```

Refresh `/buy` (or watch the SSE-driven status text) — it should flip to
credited within a few seconds of the chain watcher's next poll.

## 6. Mock Tasmota (or your real arcade)

`scripts/regtest/fake_relay.pl` is standalone and reusable outside the
scripted harness — it implements enough of Tasmota's `/cm?cmnd=...`
console interface for `internal/relay/client.go` to drive, and logs every
command it receives:

```bash
perl scripts/regtest/fake_relay.pl 9001 /tmp/relay.log
```

Point an admin-created relay board's base URL at `http://127.0.0.1:9001`.
Every command hitting it — the admin console's "test fire" button, and
real redemption pulses from a customer's `/m/{slug}` redemption — gets
appended as a JSON line to `/tmp/relay.log`, so you can confirm dispatch is
actually firing without touching hardware:

```bash
tail -f /tmp/relay.log
```

When you're ready to test against real hardware, point the board's base
URL at your arcade's actual Tasmota IP instead (e.g.
`http://192.168.1.50`) — nothing else in the flow changes; `dogecade`
doesn't know or care whether it's talking to a real board or the fake one.

## Cleanup

```bash
dogecoin-cli -regtest -datadir=~/dogecade-regtest -rpcuser=dogecade -rpcpassword=changeme stop
rm -rf ~/dogecade-regtest
```
