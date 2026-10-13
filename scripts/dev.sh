#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
local_dir="$root/.local"
pg_bin="${PG_BIN:-/opt/homebrew/opt/postgresql@18/bin}"
mkdir -p "$local_dir"
case "${1:-help}" in
  init)
    command -v redis-server >/dev/null
    test -x "$pg_bin/initdb" || { echo "Set PG_BIN to your PostgreSQL bin directory."; exit 1; }
    if [ ! -f "$root/.env.local" ]; then
      cp "$root/.env.example" "$root/.env.local"
      chmod 600 "$root/.env.local"
      secret="$(openssl rand -base64 32)"
      APP_NEW_SECRET="$secret" node --input-type=module - "$root/.env.local" <<'JS'
import fs from 'node:fs';
const path = process.argv[2];
fs.writeFileSync(path, fs.readFileSync(path, 'utf8').replace('APP_SECRET=\n', `APP_SECRET=${process.env.APP_NEW_SECRET}\n`));
JS
    fi
    if [ ! -f "$local_dir/postgres/PG_VERSION" ]; then
      "$pg_bin/initdb" -D "$local_dir/postgres" -A trust --locale=C -E UTF8 > "$local_dir/initdb.log"
    fi
    if ! "$pg_bin/pg_isready" -h 127.0.0.1 -p 55432 -d postgres >/dev/null 2>&1; then
      "$pg_bin/pg_ctl" -D "$local_dir/postgres" -l "$local_dir/postgres.log" -o "-h 127.0.0.1 -p 55432 -k '$local_dir'" start
    fi
    for database_name in tokenswoop tokenswoop_mainnet; do
      if [ "$("$pg_bin/psql" -h 127.0.0.1 -p 55432 -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$database_name'")" != 1 ]; then
        "$pg_bin/createdb" -h 127.0.0.1 -p 55432 "$database_name"
      fi
    done
    if ! redis-cli -h 127.0.0.1 -p 6389 ping >/dev/null 2>&1; then
      redis-server --bind 127.0.0.1 --port 6389 --protected-mode yes --daemonize yes --dir "$local_dir" --pidfile "$local_dir/redis.pid" --logfile "$local_dir/redis.log"
    fi
    echo "Local PostgreSQL :55432 and Redis :6389 are ready. Configure .env.local, then run make api, make worker, and make web in separate terminals."
    ;;
  api|worker|test)
    test -f "$root/.env.local" || { echo "Run make init first."; exit 1; }
    # A one-off CHAIN_NETWORK=mainnet make api override takes precedence over
    # the local file. Normally only change CHAIN_NETWORK inside .env.local.
    network_override="${CHAIN_NETWORK:-}"
    set -a
    source "$root/.env.local"
    set +a
    if [ -n "$network_override" ]; then export CHAIN_NETWORK="$network_override"; fi
    cd "$root/backend"
    export GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/gomod" GOPATH="$PWD/.cache/gopath"
    if [ "$1" = test ]; then
      exec node "$root/scripts/test-backend.mjs"
    else
      exec go run "./cmd/$1"
    fi
    ;;
  stop)
    if [ -f "$local_dir/postgres/PG_VERSION" ]; then "$pg_bin/pg_ctl" -D "$local_dir/postgres" stop -m fast || true; fi
    if [ -f "$local_dir/redis.pid" ]; then kill "$(cat "$local_dir/redis.pid")"; fi
    ;;
  *) echo "Usage: bash scripts/dev.sh {init|api|worker|test|stop}" ;;
esac
