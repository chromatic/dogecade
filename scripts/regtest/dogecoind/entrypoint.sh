#!/bin/sh
# dogecoind doesn't read RPC credentials from the environment itself (only
# from -rpcuser/-rpcpassword flags or a dogecoin.conf file), so this
# expands DOGECOIN_RPC_USER/DOGECOIN_RPC_PASSWORD into flags before
# starting it — lets a docker-compose service set plain env vars instead of
# baking a config file into the image or a bind-mounted volume.
#
# It also bootstraps the chain on startup: creates the wallet (if the
# build supports createwallet — some don't, see below) and mines up to 101
# blocks if the chain doesn't have that many yet, so `docker compose up`
# alone gets you a node past initialblockdownload with spendable coinbase
# output, no manual dogecoin-cli dance required (regtest needs 100
# confirmations before coinbase output is spendable, and dogecade's own
# node-health check treats initialblockdownload=true as "still syncing"
# and pauses purchases until enough blocks with recent timestamps exist).
# Safe to run on every restart: it only mines the shortfall, not a fresh
# 101 blocks every time, so an already-bootstrapped /data volume is a
# no-op here.
set -eu

RPC_USER="${DOGECOIN_RPC_USER:-regtest}"
RPC_PASSWORD="${DOGECOIN_RPC_PASSWORD:-regtest}"
RPC_PORT="${DOGECOIN_RPC_PORT:-18332}"
P2P_PORT="${DOGECOIN_P2P_PORT:-18444}"
ZMQ_PORT="${DOGECOIN_ZMQ_PORT:-28332}"

cli() {
    dogecoin-cli -regtest -rpcport="${RPC_PORT}" -rpcuser="${RPC_USER}" -rpcpassword="${RPC_PASSWORD}" "$@"
}

dogecoind \
    -regtest \
    -datadir=/data \
    -rpcuser="${RPC_USER}" \
    -rpcpassword="${RPC_PASSWORD}" \
    -rpcport="${RPC_PORT}" \
    -rpcbind=0.0.0.0 \
    -rpcallowip=0.0.0.0/0 \
    -port="${P2P_PORT}" \
    -zmqpubrawtx="tcp://0.0.0.0:${ZMQ_PORT}" \
    -zmqpubhashblock="tcp://0.0.0.0:${ZMQ_PORT}" \
    -fallbackfee=0.001 \
    -printtoconsole \
    "$@" &
DOGECOIND_PID=$!

# Forward shutdown signals to dogecoind rather than killing it out from
# under itself — it needs a clean -regtest shutdown to flush its DB files.
trap 'kill -TERM "$DOGECOIND_PID" 2>/dev/null; wait "$DOGECOIND_PID"' TERM INT

echo "entrypoint: waiting for dogecoind RPC..."
i=0
until cli getblockchaininfo >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
        echo "entrypoint: dogecoind RPC never became ready after 60s" >&2
        break
    fi
    # dogecoind may have exited already (e.g. bad flags); don't spin forever.
    kill -0 "$DOGECOIND_PID" 2>/dev/null || break
    sleep 1
done

if kill -0 "$DOGECOIND_PID" 2>/dev/null; then
    # Some dogecoind builds auto-load a single default wallet.dat at startup
    # and don't register the createwallet RPC at all (error -32601 Method
    # not found) — harmless, the rest of this only needs *a* loaded wallet,
    # not one created by this specific call.
    cli createwallet regtest >/dev/null 2>&1 || true

    blocks=$(cli getblockchaininfo 2>/dev/null | sed -n 's/.*"blocks": *\([0-9]*\).*/\1/p')
    blocks=${blocks:-0}
    if [ "$blocks" -lt 101 ]; then
        need=$((101 - blocks))
        echo "entrypoint: chain has ${blocks} blocks, mining ${need} more..."
        mine_addr=$(cli getnewaddress)
        cli generatetoaddress "$need" "$mine_addr" >/dev/null
        echo "entrypoint: chain bootstrapped (mined to ${mine_addr})"
    else
        echo "entrypoint: chain already has ${blocks} blocks, skipping bootstrap"
    fi
fi

wait "$DOGECOIND_PID"
