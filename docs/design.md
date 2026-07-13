# Dogecade: Design Document

A payment system for a Dogecoin-powered arcade, implementing the designs from
`chapter_dogecade.pod`. Customers buy tokens with Dogecoin (or pay a machine
directly) and redeem them for game credits, delivered to real hardware as
relay pulses.

Status: **draft — assumptions pending approval** (see final section).

## Goals

- Single small(ish) Go binary, distributed as a Docker image.
- SQLite storage on a bind-mounted volume — no external database server.
- Mobile-first customer web UI; desktop/tablet admin console.
- Token economy as the primary payment model (chapter: `manage_tokens`), with
  direct pay-to-machine (chapter: `associate_addresses_to_machines`) as a
second payment path sharing the same infrastructure.
- Dogecoin Core as the initial chain-detection backend, behind an interface
  that permits swapping in libdogecoin SPV later.
- Address supply via operator-generated batches imported **watch-only** into
  Dogecoin Core (no spend keys anywhere in the deployment), behind an
  interface that permits swapping in libdogecoin xpub derivation later.
- No CGO in v1 (a consequence of the two points above): pure-Go build,
  `CGO_ENABLED=0`, fully static binary, minimal ("serverless"/distroless)
  final image.
- OIDC for customer sign-in — no first-party password management.
- Confirmation policy configurable per deployment.

## Non-goals (v1)

- Sending Dogecoin / refunds on-chain. The service is receive-only; the
  operator sweeps funds with their own wallet. Keeps spend keys out of the
  deployment entirely.
- Multi-location support. Schema leaves room (see `machines`), but v1 is one
  arcade.
- Event ticketing (`sell_event_admission`). The deposit-address machinery
  makes it a natural follow-on, but it's out of scope for v1.
- Fiat price pegging. Token price is set in DOGE by the admin; an exchange
  rate display can come later.

## System overview

```
                     ┌────────────────────────────────────────────┐
                     │                dogecade (Go)               │
  Customer phone ───▶│   Customer UI    Admin UI     JSON API     │
  Operator laptop ──▶│  ┌──────────────────────────────────────┐  │
                     │  │               Services               │  │
                     │  │  accounts · tokens · machines ·      │  │
                     │  │  deposits · redemptions · rotation   │  │
                     │  └──────────────────────────────────────┘  │
                     │  ┌───────────┐  ┌───────────┐  ┌────────┐  │
                     │  │ChainWatch │  │  Keyring  │  │ Relays │  │
                     │  │(interface)│  │(interface)│  │ (HTTP) │  │
                     │  └─────┬─────┘  └─────┬─────┘  └────┬───┘  │
                     │        │              │      │      │      │
                     └────────┼──────────────┼──────┼──────┼──────┘
                              │              │      │      │
                     ┌────────▼──────────────▼─┐ ┌──▼───┐ ┌▼──────────┐
                     │     Dogecoin Core       │ │SQLite│ │ ESP8266   │
                     │ (RPC + ZMQ + watch-only │ │(vol.)│ │ relay     │
                     │  wallet)                │ │      │ │ boards    │
                     └─────────────────────────┘ └──────┘ └───────────┘
```

One process, several concerns:

1. **HTTP server** — customer UI, admin UI, JSON API. Server-rendered
   templates (embedded via `go:embed`) with minimal progressive-enhancement
   JS; no SPA framework, no node build step.
2. **Chain watcher** — a goroutine consuming the `ChainWatcher` interface,
   turning on-chain activity into `deposits` rows and ledger credits.
3. **Relay dispatcher** — a worker that turns redemptions into Tasmota HTTP
   commands, with retry and failure marking.
4. **Address pool / rotation** — background maintenance of a pool of fresh
   receive addresses (drawn from the Keyring backend) and, for direct-pay
   mode, per-machine active-address rotation
   (chapter: `rotate_machine_addresses`).

## Payment model

### Token economy (primary)

Chapter section: `manage_tokens`.

- A customer signs in (OIDC) and taps **Buy tokens**. The service assigns a
  fresh, never-used deposit address from the HD pool, bound to that customer,
and shows a QR code (`dogecoin:ADDRESS?amount=X`).
- The chain watcher sees a transaction to that address. Once it reaches the
  configured confirmation threshold, the deposit is credited: `tokens =
floor(received_doge / token_price_doge)`.
- Token balance lives in an append-only ledger (`token_ledger`); balance is the
  sum of a customer's entries. No mutable balance column — auditability for
free, and it matches the chapter's "think in sets" and double-entry accounting
advice.
- Deposit addresses are single-use per purchase intent but remain watched
  forever: a late or repeat payment to an old address still credits the
customer it was bound to (better than losing someone's money).
- To play: customer scans the QR on a machine → `/m/{machine-slug}` → taps
  **Insert credit** → ledger debit + relay pulse. Alternate route: customer
browses list of machines from web app, selects machine, taps **Insert credit**.
Redemption is instant; the chain is only involved at purchase time.

### Direct pay-to-machine (secondary)

Chapter sections: `associate_addresses_to_machines`, `rotate_machine_addresses`.

- A machine may be flagged `direct_pay_enabled`. It then always has exactly
  one **active** deposit address, drawn from the same HD pool, shown as a QR
  code on/near the machine (the machine page also shows it, so one printed
  QR can serve both modes).
- A confirmed payment of at least `direct_play_price_doge` to the active
  address triggers the machine's relay directly:
  `credits = floor(received / price)`, pulsed sequentially.
- The rotation job retires the active address and activates a fresh one on a
  configurable schedule and/or after N uses. Old addresses remain watched
  and still credit plays (same late-payment rule as above).
- No account required — this is the anonymous, "cool factor" path. Both paths
  converge on the same deposit-detection and relay-dispatch code.

## Chain detection

### Interface

```go
// ChainWatcher reports transactions paying watched addresses.
type ChainWatcher interface {
    // Watch adds addresses to the watched set (idempotent, persistent
    // across restarts via re-registration at startup).
    Watch(ctx context.Context, addrs ...string) error
    // Notifications delivers payment events, including confirmation
    // updates for previously seen txids.
    Notifications() <-chan PaymentEvent
    // Rescan requests a re-check from a given block height (admin tool,
    // disaster recovery).
    Rescan(ctx context.Context, fromHeight int64) error
}

type PaymentEvent struct {
    Address       string
    TxID          string
    Vout          uint32
    AmountKoinu   int64  // 1 DOGE = 1e8 koinu; integers only, everywhere
    Confirmations int
    BlockHeight   int64  // 0 while unconfirmed
}
```

### Backend 1: Dogecoin Core (v1)

- The node is **operator-managed infrastructure, deliberately outside this
  deployment** — dogecade never ships or supervises it. The admin configures
  the RPC connection (URL, credentials, optional ZMQ endpoint) in the admin
  console; settings are stored in SQLite, testable with a "check connection"
  button, and env vars may seed them at first boot. An unconfigured or
  unreachable node degrades visibly (dashboard alert; purchases pause,
  redemptions of existing tokens keep working).
- ZMQ `rawtx`/`hashblock` subscriptions provide low-latency nudges, with RPC
  polling as the reliable fallback (ZMQ is best-effort and drops messages).
- All pool addresses are registered with the node as **watch-only**
  (`importaddress`) at load time — the node holds no spend keys. Deposits
  are read back with `listsinceblock` / `gettransaction`
  (`include_watchonly`).
- On each new block (and on a slow poll timer as backstop), the watcher
  re-checks unconfirmed and sub-threshold deposits and emits updated
  `PaymentEvent`s until each deposit crosses the confirmation threshold.

### Backend 2: libdogecoin SPV (later)

- Same interface, implemented over libdogecoin's SPV client. Note this
  backend (re)introduces CGO, which also changes the build/image story —
  another reason it's deferred alongside libdogecoin key derivation.
- The deposit pipeline is backend-agnostic: everything downstream of
  `PaymentEvent` is identical.

### Confirmation policy

Chapter tip: ~1 min average block time; 0-conf is a real but bounded risk
for small amounts.

Settings (admin-editable, stored in `settings`):

- `min_confirmations` (default **1**) — threshold for crediting.
- `zero_conf_max_koinu` (default **0** = disabled) — if > 0, deposits up to
  this amount credit on mempool acceptance; larger deposits wait for
  `min_confirmations`.

A deposit's lifecycle: `seen` → `confirmed` → `credited` (or `orphaned` if
its block is reorged away before crediting — credited deposits are **not**
clawed back automatically; a reorg past the crediting threshold raises an
admin alert instead, since tokens may already be spent).

## Keys and address derivation

Chapter sections: `derive_more_addresses`, `practice_safe_wallet_hygiene`.

Address generation sits behind a small interface:

```go
// Keyring replenishes the pool of fresh, never-used receive addresses.
// Assignment itself is a pool operation (SQLite), not a Keyring call.
type Keyring interface {
    // Replenish makes n more addresses available to the pool. The batch
    // backend drains operator-imported addresses; the libdogecoin backend
    // derives them on demand.
    Replenish(ctx context.Context, n int) ([]string, error)
    ValidateAddress(ctx context.Context, addr string) (bool, error)
}
```

### Backend 1: operator-supplied batches, imported watch-only (v1)

This is the chapter's original design (`associate_addresses_to_machines`:
"addresses, maybe exported from a trusted node or generated from a secure
HD key — prepare these in a text file to import"), and it keeps **all spend
keys out of the deployment entirely**:

- The operator generates a batch of addresses **offline** — from their own
  HD wallet, a hardware wallet, an air-gapped node, any tool they trust —
  and loads them into dogecade (admin console upload or
  `dogecade addresses import <file>`).
- Dogecade validates each address, registers it with the node via
  `importaddress <addr> <label> false` (watch-only, no rescan for fresh
  never-used addresses; a batch of old addresses triggers one rescan at the
  end), and adds it to the pool. Labels record the eventual assignment.
- The Keyring never generates anything at runtime; assignment pops the next
  unassigned pool address. The node's wallet is **pure watch-only**:
  full deposit visibility (`listsinceblock`/`gettransaction` with
  watch-only included), zero custody. A stolen server + stolen node
  leak address linkage (privacy), not funds (security).
- Tradeoff: replenishment is a deliberate operator action, not automatic.
  The pool's low-water mark raises an admin alert ("load more addresses")
  rather than self-refilling. That manual step *is* the security boundary —
  it's the same moment the operator's offline wallet backup gets refreshed.
- (A convenience alternative — `getnewaddress` against a spending wallet on
  the node — would automate replenishment at the cost of putting spend keys
  in `wallet.dat` on the deployment. Rejected for v1; the manual batch step
  is cheap and the custody win is large.)

### Backend 2: libdogecoin xpub derivation (deferred)

- BIP-44 path `m/44'/3'/0'/0/i`, derived in-process via libdogecoin (CGO).
  The service holds only an account-level **xpub**; the master private key
  is generated offline (`dogecade keygen`, air-gapped) and never touches the
  deployment — the chapter's "derive from a public key only" recommendation.
- **Same custody posture as backend 1** (no spend keys deployed); what it
  adds is automation — the pool self-refills by deriving the next indexes,
  removing the manual batch-load step. That convenience is what's deferred
  with libdogecoin, not any security property.
- Derivation index tracked in `hd_cursor`; addresses stored with index +
  path so an offline wallet can always re-derive spend keys. Uses the same
  `importaddress` watch-only registration as backend 1.

### Address pool

Addresses enter the system in **batches ahead of demand**, never in the
request path. Assigning an address to a customer or machine is a single
UPDATE — no node round-trip, no key material, no realtime dependency while
someone stands at a cabinet.

Pool levels are a sizing question: the unassigned count only needs to cover
demand between operator batch loads. Thresholds (settings, defaults: warn
below 25, urgent below 10) drive admin alerts — dashboard banner first,
escalating as the pool drains. Because v1 replenishment is a human action,
alerts fire *early*: an empty pool means new customers can't start a
purchase (existing balances and redemptions are unaffected), so the
dashboard makes pool depth impossible to miss.

Batch loads give the operator a natural rhythm: generate a batch offline,
refresh the offline wallet backup, load the batch. Each load records who,
when, and how many (an `address_batches` row) for auditability.

The pool is backend-agnostic: rows in `addresses` don't care which Keyring
produced them, and the deferred libdogecoin backend simply turns
replenishment into an automatic derivation step.

## Authentication

### Customers: OIDC

- `coreos/go-oidc` + `golang.org/x/oauth2`. v1 providers: **Google** and one
  **generic OIDC** configuration (issuer URL + client credentials — covers
  Auth0, Keycloak, Dex, etc.). Facebook is OAuth2-with-OIDC-quirks; deferred.
- Privacy (chapter: `manage_tokens` risks): we store
  `sha256(issuer || subject)` as the account key — no email, no name, no
  profile data at rest. The session cookie (signed, HttpOnly, SameSite=Lax)
  carries the account id. Consequence to accept: **we cannot email
  customers**; support flows happen in person via the admin console.
- Losing access to the OIDC account = losing the token balance, mitigated by
  an admin "merge/credit account" tool.

### Operators: admin console

- Also OIDC, same flow. Admin role is granted by allowlist: the operator
  seeds `DOGECADE_ADMIN_SUBJECTS` (issuer+subject pairs, or first-login
  bootstrap: the first account to sign in when zero admins exist gets
  admin, printed loudly in the log). Admin routes additionally require the
  role; there is no separate password realm to manage.

## Hardware / relay integration

Chapter sections: `program_real_buttons`, `flip_a_switch`.

- This is the preferred path, but anything that accepts a relay pulse is
  compatible. The chapter's Tasmota-based ESP8266 boards are useful.
- Tasmota-flashed ESP8266 boards on the LAN, HTTP commands: 1. `Backlog
  PulseTime{n} 2; Power{n} ON` — set pulse width then fire, in one request, per
the chapter's advice to re-set PulseTime before every pulse (boards forget
config on reboot).
- Schema separates the board from the channel: a `relay_boards` row (name, base
  URL/hostname) and per-machine (board, relay number) binding — the chapter's
`addresses_to_relays`, normalized.
- The relay dispatcher is a queue worker: a redemption inserts a
  `credit_pulses` row (`pending`); the worker sends the HTTP command with
timeout + limited retries; success → `sent`, exhaustion → `failed` +
**automatic ledger refund** of the token and an admin alert. Multiple pending
pulses for one machine are spaced by a configurable gap (default 750 ms) so the
cabinet's scan matrix registers each one.
- Admin console gets a **Test fire** button per machine and a health check
  (periodic Tasmota `Status` query) surfacing offline boards — the chapter's
"regular diagnostics" advice.

## Data model (SQLite)

Integers for money throughout (koinu). `STRICT` tables, WAL mode,
`foreign_keys=ON`. Migrations embedded and applied at startup.

**Implementation note (learned in Phase 2):** with `foreign_keys=ON`,
SQLite requires an FK's referenced table to already exist for *any* insert
into the child table, even when the FK column value is `NULL` — it's not
just enforcement-on-write-of-non-null-values, forward-referencing a
not-yet-created table breaks inserts outright. Since this schema is applied
incrementally (one phase's tables at a time, per `plan.md`), columns that
reference a table created in a later phase (e.g. `addresses.user_id` /
`addresses.machine_id` / `address_batches.loaded_by` → `users`/`machines`,
built in Phase 4) are declared as plain nullable columns *without* a
`FOREIGN KEY` clause until that referenced table exists. A later migration
can add the constraint by rebuilding the table (SQLite has no `ALTER TABLE
... ADD CONSTRAINT`) once it's safe to do so — treat the relationship as an
application-level invariant in the meantime.

```sql
users(id, subject_hash UNIQUE, display_name, is_admin, created_at)

machines(id, slug UNIQUE, name, direct_pay_enabled,
         direct_play_price_koinu, is_active, created_at)

relay_boards(id, name, base_url, last_seen_at, is_active)
machine_relays(machine_id → machines, board_id → relay_boards,
               relay_number, is_active)

address_batches(id, source_note, address_count, loaded_by → users,
                loaded_at)                     -- audit trail of batch loads

addresses(id, address UNIQUE, batch_id NULL → address_batches,
          hd_index NULL UNIQUE, hd_path NULL,  -- libdogecoin backend only
          state,            -- pool | assigned | retired
          purpose,          -- token_deposit | machine_direct
          user_id NULL → users, machine_id NULL → machines,
          assigned_at, retired_at)

deposits(id, address_id → addresses, txid, vout, amount_koinu,
         confirmations, block_height, state,  -- seen|confirmed|credited|orphaned
         credited_at, UNIQUE(txid, vout))

token_ledger(id, user_id → users, delta,      -- +credit / -debit
             kind,       -- purchase | redemption | refund | admin_adjust
             deposit_id NULL, pulse_id NULL, note, created_at)

credit_pulses(id, machine_id → machines, user_id NULL,
              source,     -- token_redemption | direct_pay
              state,      -- pending | sent | failed
              attempts, last_error, created_at, sent_at)

settings(key PRIMARY KEY, value)   -- token_price_koinu, min_confirmations,
                                   -- zero_conf_max_koinu, rotation_interval, ...
hd_cursor(next_index)              -- single row; libdogecoin backend only
```

## Web UI

### Customer (mobile-first)

Routes: `/` (balance + buy button), `/buy` (QR + live payment status via
polling/SSE), `/m/{slug}` (machine page: insert credit, or direct-pay QR),
`/history`. Big tap targets, works over the arcade wifi, no app install.
The buy page keeps polling so the customer watches "payment seen →
confirming (1/1) → 100 tokens credited" happen live.

### Admin (desktop/tablet)

`/admin`: dashboard (recent deposits, balances, board health, alerts);
machines CRUD + relay binding + test-fire; address pool status + manual
rotation; deposits browser; user lookup + manual ledger adjustments (the
"lost my account" tool); settings editor. Server-rendered, table-heavy,
no framework.

## Project layout

```
cmd/dogecade/            main; subcommands: serve, addresses import, rescan
                         (keygen: later, with the libdogecoin backend)
internal/
  chain/                 ChainWatcher iface; corerpc/; spv/ (later)
  keyring/               Keyring iface; corewallet/; libdogecoin/ (later)
  store/                 SQLite, migrations, queries
  services/              accounts, tokens, deposits, machines, redemption, rotation
  relay/                 Tasmota client + dispatcher
  web/                   handlers, templates/, static/ (go:embed), oidc
docs/                    this file; chapter_dogecade.pod
```

## Deployment

- Multi-stage Dockerfile: standard `golang` build stage with
  `CGO_ENABLED=0` and `-ldflags="-s -w"`; final stage
  `gcr.io/distroless/static` (or `scratch` + CA certs + tzdata — the certs
  are required for OIDC calls to the provider). Image size ≈ binary size.
  Honest sizing: the Go runtime + net/http + OIDC + pure-Go SQLite
  (`modernc.org/sqlite`) lands the stripped binary around 15–20 MB.
  Getting under ~10 MB would mean giving up the embedded pure-Go SQLite
  (back to CGO) or the stdlib HTTP stack — not worth it. If single-digit MB
  ever becomes a hard requirement, a static musl CGO build of
  `mattn/go-sqlite3` in `scratch` is the escape hatch.
- Single-service `docker-compose.yml` (or a plain `docker run` line):
  just `dogecade`, with SQLite at a bind-mounted path
  (`/data/dogecade.db`) and relay boards reachable on the host LAN
  (`network_mode: host`, or routed bridge — operator's call, documented).
  **No bundled Dogecoin node**: the node is separate infrastructure the
  operator runs and maintains; dogecade only holds its RPC coordinates.
- Config via env: `DOGECADE_DB_PATH`, `DOGECADE_BASE_URL`, OIDC client
  settings, `DOGECADE_ADMIN_SUBJECTS`. Node RPC/ZMQ connection details are
  configured in the admin console (env vars `DOGECOIND_RPC_URL/USER/PASS`,
  `DOGECOIND_ZMQ_ADDR` may seed the settings at first boot).
  (`DOGECADE_XPUB` arrives with the libdogecoin Keyring backend.)
- Backup = copy the SQLite file (documented `sqlite3 .backup` cron example).
  The node's wallet is watch-only and rebuildable (re-import the addresses
  from SQLite + rescan), so it needs no custody-grade backup; the spend keys
  live wherever the operator generated them, offline.

## Implementation plan

Each phase ends runnable and demoable. Detailed task breakdown with
acceptance criteria: `docs/plan.md`.

1. **Skeleton + storage.** Module, `cmd/dogecade serve`, config loading,
   SQLite store with migrations, settings service, CI (`go vet`, `go test`,
   `golangci-lint`). Healthcheck endpoint.
2. **Keyring + pool.** Interface + batch-import backend
   (`dogecade addresses import`, validation, watch-only `importaddress`
   registration, `address_batches` audit rows); pool service with
   low-water alerts. Tested against a regtest node (shared harness with
   phase 3).
3. **Chain watcher (Core RPC).** Interface + Core implementation
   (regtest-based integration tests: spin up dogecoind in regtest, mine
   blocks, assert PaymentEvents). Deposit pipeline: seen → confirmed →
   credited with configurable thresholds.
4. **Token core.** Users table, ledger, purchase flow wired to deposits;
   redemption service (debit + pulse row). Still API/CLI-only.
5. **Relay dispatcher.** Tasmota client, pulse worker, retries, refund on
   failure, board health polling. Test-fire CLI. (Hardware-in-the-loop
   testing happens here with a real/simulated board — Tasmota is easy to
   fake with a stub HTTP server.)
6. **OIDC + customer UI.** Login, sessions, `/`, `/buy` with live status,
   `/m/{slug}`, `/history`. Mobile styling pass.
7. **Admin console.** Dashboard, machines + relays CRUD, test-fire, pool
   management, deposit browser, ledger adjustments, settings.
8. **Direct pay + rotation.** Per-machine active address, rotation job +
   admin trigger, direct-pay crediting path into the pulse queue.
9. **Packaging.** Dockerfile (static build, distroless), compose file,
   operator docs (node setup; offline address-generation walkthrough +
   batch loading; relay flashing pointers back to the chapter),
   backup/restore doc.
10. **Later / explicitly deferred:** libdogecoin Keyring backend
    (xpub-only, air-gapped keys) + `keygen` subcommand, libdogecoin SPV
    backend, Facebook login, event admission (`sell_event_admission`),
    multi-location, fiat price display.

## Assumptions requiring approval

1. **Zero spend keys in the deployment.** Addresses are generated offline by
   the operator and imported watch-only (node included). The whole
   deployment is receive-only: no on-chain refunds, and funds are swept with
   the operator's offline wallet. The cost is a manual, alert-driven
   replenishment step — the pool running dry blocks *new* purchases until
   the operator loads another batch (redemptions unaffected). The deferred
   libdogecoin xpub backend automates replenishment with the same custody
   posture.
2. **Token price set in DOGE** by the admin (e.g. 10 DOGE/token), no fiat
   peg in v1.
3. **No PII at rest**: OIDC subject hashes only ⇒ no customer email, so no
   receipts/notifications; account recovery is an in-person admin action.
4. **Late payments to retired/old addresses still credit** the bound
   customer/machine rather than being ignored.
5. **Failed relay pulses auto-refund** the token and alert the admin (vs.
   retrying forever or eating the token).
6. **Go + stdlib-first, CGO-free stack**: server-rendered templates,
   `net/http` routing (Go ≥1.22 pattern mux), no JS framework,
   `modernc.org/sqlite` (pure Go) so the binary is fully static and the
   image can be distroless/scratch. Accepted cost: the stripped binary is
   ~15–20 MB, not single-digit — pure-Go SQLite is most of that, and
   trading it back for CGO defeats the point.
7. **Dogecoin Core is separate, operator-managed infrastructure.** Dogecade
   ships alone; the admin configures the node's RPC (and optional ZMQ)
   connection in the admin console. Dogecade must degrade gracefully when
   the node is unconfigured or down: purchases pause with a visible alert,
   token redemptions continue.
8. **Tasmota-flashed boards over HTTP** on a trusted LAN segment, per the
   chapter; MQTT support deferred.
9. **Single arcade, single currency, single token price** in v1.
10. **Reorg handling**: credited deposits are never automatically clawed
    back; reorgs raise admin alerts instead.
