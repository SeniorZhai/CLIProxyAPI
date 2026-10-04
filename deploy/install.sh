#!/usr/bin/env sh
set -eu
umask 077
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  printf '%s\n' 'Install Docker Engine and the Docker Compose plugin, then retry.' >&2
  exit 1
fi
if [ ! -f .env ]; then
  cp .env.example .env
  printf '%s\n' 'Created deploy/.env. Set CPA_PUBLIC_URL to your HTTPS origin, then rerun this script.'
  exit 1
fi
mkdir -p data
chmod 700 data
docker compose up -d --build --wait
printf '%s\n' 'Administrator console: <CPA_PUBLIC_URL>/admin/' \
  'Read initial credentials locally: sudo cat deploy/data/admin/initial-credentials.txt' \
  'The credentials file is removed after the first successful login.'
