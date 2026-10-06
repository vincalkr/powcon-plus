#!/usr/bin/env bash
# Quick demo without touching $HOME: daemon with a data dir inside the repo,
# plus a check that the label is exposed on the session bus.
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p .localdata
export POWCON_DATA_DIR="$PWD/.localdata"
: "${POWCON_LABEL_MODE:=both}"
export POWCON_LABEL_MODE

if [[ ! -x bin/powcon-plus ]]; then
  echo "building…"
  . hack/env.sh
  CGO_ENABLED=0 go build -o bin/powcon-plus ./cmd/powcon-plus
fi

bin/powcon-plus run --verbose &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT

sleep 3
echo "--- status ---"
bin/powcon-plus status

NAME="$(busctl --user --no-pager list 2>/dev/null |
  awk '/org\.kde\.StatusNotifierItem-[0-9]+-1/{print $1}' | head -1)"
if [[ -n "$NAME" ]]; then
  echo "--- tray label ($NAME) ---"
  gdbus call --session --dest "$NAME" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get \
    org.kde.StatusNotifierItem XAyatanaLabel
else
  echo "no item registered on the bus: the tray icon is not available"
fi

echo
echo "Press Enter to end the demo."
read -r _
