#!/usr/bin/env bash
# Run against an installed test instance; creates and removes one AWG interface.
set -euo pipefail

[[ $# -eq 2 ]] || { echo "Usage: $0 MANAGEMENT_PORT UDP_START-UDP_END" >&2; exit 1; }
[[ "$1" =~ ^[0-9]+$ && "$2" =~ ^([0-9]+)-([0-9]+)$ ]] || exit 1
PORT="$1"
RANGE="$2"
START="${BASH_REMATCH[1]}"
END="${BASH_REMATCH[2]}"
BASE_URL="http://127.0.0.1:$PORT"
DB=/etc/wireguard/data/cascade.db
INTERFACE=""
cleanup() {
  if [[ -n "$INTERFACE" ]]; then
    curl -fsS --max-time 30 -X DELETE "$BASE_URL/api/tunnel-interfaces/$INTERFACE" >/dev/null
  fi
}
trap cleanup EXIT

curl -fsS --max-time 5 "$BASE_URL/api/health" | grep -q '"status":"ok"'
HEALTH_STATUS=""
for ((attempt = 0; attempt < 45; attempt++)); do
  HEALTH_STATUS=$(docker inspect cascade --format '{{.State.Health.Status}}')
  [[ "$HEALTH_STATUS" != healthy ]] || break
  sleep 1
done
[[ "$HEALTH_STATUS" == healthy ]]
[[ "$(docker exec cascade sqlite3 "$DB" "SELECT value FROM settings WHERE key='portPool';")" == "$RANGE" ]]
[[ "$(docker exec cascade sqlite3 "$DB" 'PRAGMA quick_check;')" == ok ]]
[[ "$(docker inspect cascade --format '{{.HostConfig.NetworkMode}}')" != host ]]
[[ "$(docker exec cascade printenv WG_QUICK_USERSPACE_IMPLEMENTATION)" == amneziawg-go ]]
BINDINGS=$(docker inspect cascade --format '{{json .HostConfig.PortBindings}}')
for ((port = START; port <= END; port++)); do
  grep -q "\"$port/udp\"" <<< "$BINDINGS"
done

RESPONSE=$(curl -fsS --max-time 30 -H 'Content-Type: application/json' \
  -d '{"name":"Installation smoke test","protocol":"amneziawg-3.1"}' \
  "$BASE_URL/api/tunnel-interfaces/quick-create")
INTERFACE=$(sed -n 's/.*"id":"\([a-zA-Z0-9_-]*\)".*/\1/p' <<< "$RESPONSE")
[[ -n "$INTERFACE" ]]
grep -q '"started":true' <<< "$RESPONSE"
LISTEN_PORT=$(docker exec cascade sqlite3 "$DB" "SELECT listen_port FROM interfaces WHERE id='$INTERFACE';")
[[ "$LISTEN_PORT" -ge "$START" && "$LISTEN_PORT" -le "$END" ]]
docker exec cascade ip link show "$INTERFACE" | grep -q 'UP'
PROCESSES=$(docker top cascade -eo pid,args)
grep -q "amneziawg-go $INTERFACE" <<< "$PROCESSES"
echo "Bridge installation passed: PortPool=$RANGE, userspace interface=$INTERFACE, listen port=$LISTEN_PORT."
