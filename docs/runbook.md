# Dogecade Operator Runbook

Operating notes for running a `dogecade` deployment day to day: what the
Dogecoin node needs, how to load address inventory, how backups work, and
what to do when an alert fires.

## Dogecoin node requirements

`dogecade` is receive-only: it never holds spend keys and never sends a
transaction. It talks to `dogecoind` over RPC (address watch, block/tx
lookup) and, optionally, ZMQ (fast payment notification). A minimal
`dogecoin.conf`:

```ini
server=1
rpcuser=dogecade
rpcpassword=<generate a long random value>
rpcallowip=127.0.0.1
# Only needed if dogecade and dogecoind aren't on the same host/network
# namespace:
# rpcbind=0.0.0.0

# Recommended: push new blocks/txs to dogecade instead of relying purely on
# RPC polling.
zmqpubrawtx=tcp://127.0.0.1:28332
zmqpubhashblock=tcp://127.0.0.1:28332
```

`-txindex` is **not** required — deposit detection walks `importaddress`'d
watch-only addresses via `listsinceblock`/wallet notifications, not
arbitrary transaction lookups. No wallet passphrase/encryption is relevant
since the node never signs anything on dogecade's behalf; the addresses it
tracks are watch-only.

Point dogecade at the node with:

```
DOGECOIND_RPC_URL=http://<host>:22555
DOGECOIND_RPC_USER=dogecade
DOGECOIND_RPC_PASS=<same as rpcpassword above>
DOGECOIND_ZMQ_ADDR=tcp://<host>:28332   # optional but recommended
```

These are seeded into the `settings` table on first boot
(`SettingsService.SeedFromEnv`) and are not overwritten on later restarts,
so changing them afterward means editing the row directly or the admin
settings page, not just changing the env var.

If `DOGECOIND_RPC_URL` is left unset, dogecade still starts: address imports
land in a pending state, deposit detection sits idle, and the node health
check reports unreachable. This is the expected state for local development
or the first few minutes of a fresh deployment before the node is wired up.

## Generating and loading addresses

dogecade never derives keys itself. Address generation is an **offline,
air-gapped** step using `Finance::Libdogecoin` (or the language binding of
your choice) as described in the book chapter's `derive_more_addresses`
walkthrough: derive a batch of addresses from your HD wallet's `xpub` under
`m/44'/3'/0'/0/index`, on a machine that never touches the network, and
carry only the resulting *public* addresses over to the dogecade host — the
master/private key never leaves the offline machine.

Once you have a plain text file of addresses (one per line, blank lines and
`#`-prefixed comments ignored), load it with the CLI:

```
dogecade addresses import --purpose=token_deposit ./batch-2026-07.txt
dogecade addresses import --purpose=machine_direct ./direct-pay-batch.txt
```

Use `token_deposit` for the shared customer top-up pool and `machine_direct`
for the per-machine direct-pay pool (Phase 8). The importer calls
`importaddress` against the configured node for each address (marking it
watch-only, no rescan) so deposits start being detected immediately; if no
node is configured yet, addresses import in a "pending node registration"
state and get registered lazily once the node is reachable.

Keep the pool topped up before it runs dry:

- `pool_warn_threshold` (default 25) / `pool_urgent_threshold` (default 10)
  — remaining unused `token_deposit` addresses below these trigger
  `pool_low_warn` / `pool_low_urgent` alerts (see below). Import a fresh
  batch well before you hit the urgent line.
- The `machine_direct` pool needs at least one spare address per
  direct-pay-enabled machine at all times, or rotation can't proceed (see
  `direct_pay_pool_empty` below).

## Sweeping funds

dogecade is deliberately receive-only — it holds no spend keys, so it cannot
sweep or forward funds itself. Move accumulated Dogecoin out of the watched
addresses periodically using your own wallet (the same one whose `xpub`
generated the addresses), independent of dogecade. This is a manual
operator task outside the application.

## Backup and restore

The only state that matters is the SQLite file at `DOGECADE_DB_PATH`
(default `/data/dogecade.db` in the container). Everything else (the
`dogecoind` wallet, chain data) is rebuildable from the blockchain plus your
offline-derived `xpub`, so it does not need to be backed up with the same
urgency — but note that watch-only `importaddress` registrations on a
rebuilt node still need to be re-imported/rescanned from your address
inventory.

Back it up with SQLite's own consistent-snapshot backup, not a raw file
copy (the WAL file must be checkpointed correctly, which a plain `cp` does
not guarantee):

```
0 * * * * sqlite3 /data/dogecade.db ".backup '/backups/dogecade-$(date +\%Y\%m\%d\%H\%M).db'"
```

Restore by stopping the service, copying a backup file to
`DOGECADE_DB_PATH`, and restarting. Restoring an older backup loses any
deposits/redemptions/credit pulses recorded after that snapshot — reconcile
against the node's transaction history for the gap before resuming
operation.

## Alert catalogue

Alerts are deduplicated (an unacknowledged alert of the same kind isn't
re-inserted) and surface on the admin dashboard. Acknowledging one doesn't
fix the underlying condition — it only stops it from cluttering the list.

| Kind | Raised when | Response |
|---|---|---|
| `node_unreachable` | The Dogecoin node RPC call fails (down, wrong credentials, network partition). | Check `dogecoind` is running and `DOGECOIND_RPC_*` are correct; deposit detection is stalled until this clears. |
| `node_syncing` | The node reports it is not yet caught up with the chain tip. | Wait it out; deposits behind the node's sync point won't be seen until it catches up. |
| `pool_low_warn` | Unused `token_deposit` addresses fall below `pool_warn_threshold` (default 25). | Import another batch soon; not yet urgent. |
| `pool_low_urgent` | Unused `token_deposit` addresses fall below `pool_urgent_threshold` (default 10). | Import a fresh batch now — new customer top-ups will fail to get an address once the pool is exhausted. |
| `direct_pay_pool_empty` | Rotation (scheduled or manual) needed a replacement `machine_direct` address but none were available in the pool. | Import more `machine_direct` addresses. The machine's old address is left active (never left addressless) but keeps accumulating use/age past your rotation policy until you resolve this. |
| `relay_dispatch_failed` | A queued credit pulse exhausted `relay_max_attempts` (default 5) HTTP attempts against a relay board without success. The pulse is refunded back to the customer's balance. | Check the relay board is powered and reachable; the customer was made whole automatically, but the game credit was not delivered. |
| `relay_board_offline_<id>` | The periodic board health poller can't reach a specific relay board. | Check that board's power/network; other boards are unaffected. |

## Operator settings quick reference

All of the following are admin-editable at runtime (no restart required)
via the settings page, and are otherwise seeded once from environment
variables on first boot:

- `min_confirmations` (default 1), `zero_conf_max_koinu` (default 0, i.e.
  0-conf disabled) — confirmation policy for crediting a deposit.
- `token_price_koinu` (default 100000000, i.e. 1 DOGE/token).
- `relay_pulse_gap_ms` (default 750), `relay_max_attempts` (default 5) —
  relay dispatch pacing/retry policy.
- `direct_pay_max_credits_per_tx` (default 10), which caps how many credits
  a single oversized direct payment can generate in one shot.
- `direct_pay_rotate_interval_hours` / `direct_pay_rotate_after_uses`
  (both default 0/disabled) — direct-pay address rotation schedule; leaving
  both at 0 disables automatic rotation (manual rotation from the admin UI
  still works).
