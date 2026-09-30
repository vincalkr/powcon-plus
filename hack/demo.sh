#!/usr/bin/env bash
# Demo veloce senza toccare $HOME: daemon con data dir nel repo + verifica
# che l'etichetta sia esposta sul bus di sessione.
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p .localdata
export POWCON_DATA_DIR="$PWD/.localdata"
: "${POWCON_LABEL_MODE:=both}"
export POWCON_LABEL_MODE

if [[ ! -x bin/powcon-plus ]]; then
  echo "build in corso…"
  . hack/env.sh
  CGO_ENABLED=0 go build -o bin/powcon-plus ./cmd/powcon-plus
fi

bin/powcon-plus run --verbose &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT

sleep 3
echo "--- stato ---"
bin/powcon-plus status

NAME="$(busctl --user --no-pager list 2>/dev/null |
  awk '/org\.kde\.StatusNotifierItem-[0-9]+-1/{print $1}' | head -1)"
if [[ -n "$NAME" ]]; then
  echo "--- etichetta tray ($NAME) ---"
  gdbus call --session --dest "$NAME" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get \
    org.kde.StatusNotifierItem XAyatanaLabel
else
  echo "nessun item registrato sul bus: l'icona di tray non è disponibile"
fi

echo
echo "Premi Invio per chiudere la demo."
read -r _
