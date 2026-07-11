# Dogecade: Implementation Plan

Working breakdown of the phases in `design.md`. Each phase ends runnable and
demoable; each task is meant to be a reviewable unit of work (roughly one
PR). Checkboxes track progress.

Conventions that apply to every task:

- Money is `int64` koinu everywhere; no floats touch amounts.
- Every service method takes a `context.Context`.
- Table-driven unit tests alongside the code; integration tests build-tagged
  `//go:build integration` so `go test ./...` stays fast.
- `go vet` + `golangci-lint` clean before merge.

## Phase 1 — Skeleton + storage

Goal: `dogecade serve` starts, applies migrations, answers a healthcheck.

- [x] **1.1 Module + layout.** `go mod init`, directory skeleton from
      design.md §Project layout, `cmd/dogecade` with subcommand dispatch
      (stdlib `flag`, no CLI framework). `dogecade version` works.
- [x] **1.2 Config loading.** Env-var config struct (`DOGECADE_DB_PATH`,
      `DOGECADE_BASE_URL`, listen addr); validation with clear startup
      errors. Node RPC env vars parsed but only used to seed settings (1.4).
- [x] **1.3 Store + migrations.** `modernc.org/sqlite`; open with WAL,
      `foreign_keys=ON`, busy timeout; embedded migrations
      (`migrations/NNNN_*.sql`, applied in a transaction, recorded in
      `schema_migrations`). First migration: `settings` only — later phases
      add tables in their own migrations.
- [x] **1.4 Settings service.** Typed get/set over the `settings` table with
      defaults (e.g. `min_confirmations=1`); env seeding at first boot.
- [x] **1.5 HTTP skeleton.** `net/http` + Go 1.22 pattern mux; `/healthz`
      (checks DB); graceful shutdown on SIGTERM; structured logging
      (`log/slog`).
- [x] **1.6 CI.** GitHub Actions (or equivalent): vet, lint, test, build
      with `CGO_ENABLED=0`.

Demo: `dogecade serve` against an empty bind-mounted directory; curl
`/healthz`.

## Phase 2 — Keyring + address pool

Goal: operator can load a batch of addresses; pool tracks and alerts.

- [x] **2.1 Keyring interface + address validation.** `Keyring` iface per
      design; pure-Go Dogecoin address validation (Base58Check, version
      bytes for mainnet/testnet/regtest P2PKH + P2SH) — unit-tested against
      known-good/bad vectors. No node needed for validation.
- [x] **2.2 Schema.** Migration: `addresses`, `address_batches`, `hd_cursor`
      (empty, reserved).
- [x] **2.3 Batch import.** `dogecade addresses import <file>` (one address
      per line, `#` comments): validate all-or-nothing, insert batch + rows,
      register watch-only with the node (2.5) when configured, else mark
      `pending_import` for later registration.
- [x] **2.4 Pool service.** Assign (atomic claim of oldest pool row),
      release/retire, counts by state; low-water thresholds from settings;
      alert records (table shared with later relay/reorg alerts:
      `alerts(id, kind, message, created_at, acked_at)` — add in this
      migration).
- [x] **2.5 Node RPC client (minimal).** Hand-rolled JSON-RPC client
      (stdlib only): `getblockchaininfo`, `importaddress`,
      `validateaddress`. Connection settings read from the settings service;
      "unconfigured" is a first-class state, not an error.
- [x] **2.6 Regtest harness.** Script + Go test helper that launches
      `dogecoind -regtest` (developer/CI provides the binary; harness skips
      with a clear message if absent), creates a wallet, mines blocks.
      Build-tagged integration tests: import batch → addresses visible as
      watch-only on the node.

Demo: explain to user how to generate a file of regtest addresses; import that
file; see pool counts; drain the pool and watch the alert fire.

## Phase 3 — Chain watcher + deposit pipeline

Goal: a payment on regtest becomes a `deposits` row that reaches `credited`.

- [x] **3.1 Schema.** Migration: `deposits`.
- [x] **3.2 ChainWatcher interface + Core RPC backend.** Polling loop first:
      `listsinceblock` (watch-only included) from a persisted block cursor;
      emit `PaymentEvent`s; handle restarts idempotently
      (`UNIQUE(txid, vout)`).
- [x] **3.3 ZMQ nudges.** Optional `rawtx`/`hashblock` subscription
      (`github.com/pebbe/zmq4`? No — CGO. Use a pure-Go ZMQ reader,
      `github.com/go-zeromq/zmq4`) that just triggers an immediate poll;
      polling remains the source of truth. If no ZMQ endpoint configured,
      poll on a short timer.
- [x] **3.4 Deposit pipeline.** Consume `PaymentEvent`s: upsert deposit,
      advance `seen → confirmed` per `min_confirmations` /
      `zero_conf_max_koinu`; mark `orphaned` on reorg before crediting;
      alert on reorg after crediting (no clawback). Crediting itself is a
      stub hook until phase 4.
- [x] **3.5 Node health.** Periodic `getblockchaininfo`; node state
      (unconfigured / unreachable / syncing / ok) exposed on `/healthz` and
      as an alert; purchase flow will consult this (phase 6).
- [x] **3.6 Integration tests.** Regtest: pay a pool address → deposit
      `seen`; mine a block → `confirmed`; invalidate the block →
      reorg paths. Confirmation-policy matrix (0-conf cap on/off).

Demo: end-to-end on regtest from `sendtoaddress` to a `confirmed` deposit
row, watched live in the log.

## Phase 4 — Token core

Goal: deposits credit tokens; redemptions debit them; all API/CLI-only.

- [x] **4.1 Schema.** Migration: `users`, `token_ledger`, `machines`,
      `credit_pulses` (pulse rows created here, dispatched in phase 5).
      Note: `addresses.user_id`/`machine_id` and `address_batches.loaded_by`
      (from Phase 2) currently have no `FOREIGN KEY` clause since `users`/
      `machines` didn't exist yet (see design.md's data-model implementation
      note) — once created here, decide whether to add the constraint via a
      table rebuild or leave it as an application-level invariant.
- [x] **4.2 Ledger service.** Append-only entries; balance query; invariant
      tests (balance never negative; every debit references a pulse; every
      purchase credit references a deposit; concurrent redemption of a
      1-token balance can't double-spend — single UPDATE-guarded insert or
      `BEGIN IMMEDIATE`).
- [x] **4.3 Purchase flow.** "Start purchase" assigns a pool address to the
      user (reusing an open unassigned intent if they tap twice); deposit
      crediting hook from 3.4 lands here:
      `tokens = amount_koinu / token_price_koinu` (floor; remainder koinu
      recorded on the deposit row for auditability).
- [x] **4.4 Redemption service.** Debit one token + insert `credit_pulses`
      row atomically; machine must be active and relay-bound (checked
      here, enforced again at dispatch).
- [x] **4.5 Late-payment rule.** Payment to a retired/assigned-old address
      credits its bound user (tested).

Demo: scripted regtest run — import addresses, fake user, pay, mine,
balance appears, redeem, pulse row `pending`.

## Phase 5 — Relay dispatcher

Goal: `pending` pulse rows become Tasmota HTTP calls, with refund on failure.

- [x] **5.1 Schema.** Migration: `relay_boards`, `machine_relays`.
- [x] **5.2 Tasmota client.** `Backlog PulseTime{n} 2; Power{n} ON` request
      builder; `Status` health query; timeouts; response parsing. Unit
      tests against `httptest` stub.
- [x] **5.3 Dispatcher worker.** Claim `pending` pulses per machine in
      order; per-machine spacing gap (setting, default 750 ms); retry with
      backoff up to N attempts; exhaustion → `failed` + ledger refund
      (kind=`refund`, references the pulse) + alert. Crash-safe: claimed
      but unsent pulses recover on restart.
- [x] **5.4 Board health.** Periodic `Status` poll of active boards;
      `last_seen_at`; offline alert.
- [x] **5.5 Test-fire.** `dogecade relays test-fire <machine>` CLI (admin
      UI button comes in phase 7).
- [x] **5.6 Failure-mode tests.** Stub board: down, slow, 500s, recovers
      mid-retry; assert refund exactly-once.

Demo: stub Tasmota server on laptop (or a real board): redemption →
observable HTTP pulse; kill the stub → refund + alert.

## Phase 6 — OIDC + customer UI

Goal: a customer on a phone can sign in, buy, watch confirmation, redeem.

- [x] **6.1 OIDC.** `coreos/go-oidc`: Google + generic issuer from config;
      login/callback/logout; `sha256(issuer||subject)` account key; signed
      session cookie (HttpOnly, SameSite=Lax, `Secure` when base URL is
      https). First-login bootstrap + `DOGECADE_ADMIN_SUBJECTS` allowlist.
- [x] **6.2 Template + static pipeline.** `go:embed`, base layout,
      light/dark via `prefers-color-scheme`, no JS framework (a few small
      vanilla helpers), QR rendering server-side
      (`github.com/skip2/go-qrcode` → PNG data URI).
- [x] **6.3 Customer pages.** `/` balance + buy; `/buy` QR
      (`dogecoin:ADDR?amount=`) with live status via SSE (fallback:
      3s polling) walking seen → confirming (n/m) → credited;
      `/m/{slug}` insert-credit (+ direct-pay QR when enabled, phase 8);
      `/machines` browse list; `/history` ledger view.
- [x] **6.4 Purchase pause.** When node state isn't ok (3.5), `/buy`
      explains instead of assigning an address; redemption unaffected.
- [ ] **6.5 Mobile pass.** Real-device check: tap targets, viewport, QR
      size, arcade-wifi latency tolerance. (CSS/viewport groundwork is in
      place — 44px tap targets, responsive QR, `prefers-color-scheme` — but
      this still needs an actual phone on real arcade wifi to check off.)

Demo: phone on the LAN, full loop against regtest + stub relay. Allow admin
user/api call to issue credit tokens.

## Phase 7 — Admin console

Goal: the operator runs the arcade without touching SQL or the CLI.

- [x] **7.1 Admin shell + authz.** `/admin` layout; role check middleware;
      audit log of admin mutations (who/what/when — table `admin_audit`).
- [x] **7.2 Dashboard.** Node state, pool depth (loud when low), board
      health, unacked alerts, recent deposits/redemptions.
- [x] **7.3 Node settings page.** RPC URL/credentials, ZMQ endpoint;
      "check connection" button; settings stored via 1.4. Note: the live
      node client/chain watcher are still only constructed once at boot
      (from settings, seeded once from the environment), so saving new
      connection settings here takes effect after a restart, not
      immediately — documented on the page itself.
- [x] **7.4 Machines + relays.** CRUD, board CRUD, relay binding,
      test-fire button, enable/disable.
- [x] **7.5 Addresses.** Batch upload (textarea/file), pool browser by
      state, batch audit view, manual retire.
- [x] **7.6 Deposits + users.** Deposit browser with state filter; user
      lookup (by display name — remember: no email); balance view; manual
      ledger adjustment with required note (the "lost account" tool);
      account merge.
- [x] **7.7 Settings editor.** Token price, confirmation policy, thresholds,
      pulse gap — with validation and change audit.

Demo: fresh DB → configure node, load addresses, create machine, bind
relay, sell and redeem a token, entirely through the UI.

## Phase 8 — Direct pay + rotation

Goal: the chapter's "cool factor" path: pay the machine, it lights up.

- [x] **8.1 Machine active address.** `direct_pay_enabled` +
      `direct_play_price_koinu`; exactly-one-active-address invariant
      (partial unique index); machine page + admin show the active QR.
      Note: addresses are now imported with an explicit purpose
      (`token_deposit` or `machine_direct` — `ImportBatch`'s new `purpose`
      param, `dogecade addresses import --purpose=...`, and a selector on
      the admin import form), since the direct-pay pool draws from
      purpose='machine_direct' rows rather than the customer token pool.
- [x] **8.2 Direct crediting.** Deposit to a machine-bound address →
      `credits = amount/price` pulse rows (capped by
      `direct_pay_max_credits_per_tx`, default 10), no user account
      involved. A single `NewDirectPayAwareCreditHook` routes each deposit
      to either this or the existing purchase-credit hook based on the
      depositing address's purpose, so `DepositPipeline` only needs one
      `CreditHook`.
- [x] **8.3 Rotation job.** Retire + replace active address on interval
      and/or after N uses (settings, both default 0/disabled); manual
      rotate button in the admin machines page; pool-empty case alerts
      (`direct_pay_pool_empty`) and keeps the old address active (never
      leaves a machine addressless).
- [x] **8.4 Integration test.** Regtest:
      `TestDepositLifecycle_DirectPayAndRotation` pays a machine's active
      direct-pay address → confirms a `direct_pay` credit pulse is queued;
      rotates the address; a late payment to the now-retired old address
      still queues a pulse (late-payment rule). Build-tagged `integration`
      like the other regtest tests; skips cleanly without a `dogecoind`
      binary on PATH (confirmed in this environment).

## Phase 9 — Packaging + operator docs

Goal: `docker run` on a clean host gets a working system.

- [x] **9.1 Dockerfile.** Multi-stage, `CGO_ENABLED=0`,
      `-ldflags="-s -w"`, `gcr.io/distroless/static` (certs + tzdata);
      non-root; `/data` volume. Record actual image size in the README.
      Added a `compress` stage that runs UPX (`--best --lzma`) on the
      stripped binary: 14.4MB -> 4.7MB locally, which keeps the packed
      image around 5-7MB vs ~16MB unpacked (measured via local `go build`
      + gzip, since this sandbox has no Docker daemon access to run an
      actual `docker build`/`docker images`). Tradeoff (AV/scanner false
      positives on packed executables) is called out in the Dockerfile
      comment and README; the stage is easy to drop if not wanted.
- [x] **9.2 Compose + run docs.** `docker-compose.yml` added: single
      `dogecade` service, `network_mode: host` as the default (commented
      bridge-mode alternative with a note on reaching relay boards /
      `dogecoind` from inside the container network namespace either way),
      and a reminder that `DOGECADE_BASE_URL` needs to be a stable HTTPS
      origin (reverse proxy terminating TLS) for OIDC redirects to work.
- [x] **9.3 Operator runbook.** `docs/runbook.md` added: node requirements
      (RPC user/pass, `zmqpubrawtx`/`zmqpubhashblock`, explicitly no
      `-txindex` needed since detection is watch-only-address based, not
      arbitrary txid lookup); offline HD-derivation walkthrough (per the
      book chapter's `derive_more_addresses`) feeding `dogecade addresses
      import --purpose=token_deposit|machine_direct`; sweep procedure
      (operator's own wallet — dogecade holds no spend keys by design);
      backup/restore via `sqlite3 .backup` cron (not a raw file copy, to
      respect WAL checkpointing); and a full alert catalogue table
      (`node_unreachable`, `node_syncing`, `pool_low_warn/urgent`,
      `direct_pay_pool_empty`, `relay_dispatch_failed`,
      `relay_board_offline_<id>`) with operator responses for each.
- [x] **9.4 Release build.** `version` was already wired via
      `-ldflags -X main.version=...` (confirmed working end-to-end with
      `dogecade version`); added `.github/workflows/release.yml` which
      builds and pushes the image to GHCR on `v*.*.*` tags, stamping
      `VERSION` from the tag and publishing `:X.Y.Z`, `:X.Y`, and
      `:latest` tags via `docker/metadata-action`.

## Deferred (tracked, not scheduled)

libdogecoin Keyring backend (xpub derivation + `keygen`), libdogecoin SPV
ChainWatcher, Facebook login, event admission (`sell_event_admission`),
multi-location, fiat price display, MQTT relay transport.

## Suggested checkpoints

Natural review points to pause and reassess: after phase 3 (the riskiest
plumbing — regtest harness and deposit pipeline — is proven), after phase 5
(money in → hardware out works headless), and after phase 7 (operable
system; phases 8–9 are additive).
