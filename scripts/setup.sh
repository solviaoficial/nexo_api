#!/usr/bin/env sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
command -v docker >/dev/null
docker compose version >/dev/null
docker build -t nexo-whatsapp:local .
if [ ! -e .env ]; then
  docker run --rm --network none --user "$(id -u):$(id -g)" -v "$PWD:/setup" -w /setup nexo-whatsapp:local init-env
fi
mkdir -p data backups
chmod 700 backups
docker run --rm --network none --user 0:0 --entrypoint sh -v "$PWD/data:/data" nexo-whatsapp:local -c 'chown 10001:10001 /data && chmod 700 /data'
printf '%s\n' 'Configure DOMAIN e PUBLIC_ORIGIN no .env. Depois: docker compose up -d'
