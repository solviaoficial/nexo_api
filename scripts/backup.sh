#!/usr/bin/env bash
set -euo pipefail
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
command -v age >/dev/null || { printf '%s\n' 'Instale age e informe AGE_RECIPIENT (chave pública).'; exit 1; }
: "${AGE_RECIPIENT:?Defina a chave pública age para criptografar o backup}"
mkdir -p backups
chmod 700 backups
umask 077
stamp=$(date -u +%Y%m%dT%H%M%SZ)
target="$PWD/backups/nexo-$stamp.tar.age"
docker compose stop app
trap 'docker compose start app >/dev/null' EXIT INT TERM
# Cold snapshot of both databases, WAL files and encryption key, encrypted before writing.
# /data is mode 0700, so a short-lived root container reads it without weakening permissions.
docker run --rm --network none --user 0:0 --entrypoint tar -v "$PWD:/source:ro" nexo-whatsapp:local -C /source -czf - data .env | age -r "$AGE_RECIPIENT" -o "$target"
test -s "$target"
printf 'Backup criptografado: %s\n' "$target"
