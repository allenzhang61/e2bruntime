#!/usr/bin/env bash
set -euo pipefail
cd /opt/e2b-runtime
install -d -m 700 custom backups
install -m 755 /tmp/api /tmp/orchestrator /tmp/db-migrator custom/
install -m 755 /tmp/runtime.sh runtime.sh
install -m 755 /tmp/orchestrator-launch.sh custom/
umask 077
log=/var/log/e2b-runtime-source-install.log
touch "$log";chmod 600 "$log"
# Capture current database before applying migrations from the same source as API.
docker compose exec -T postgres pg_dump -U postgres postgres </dev/null > "backups/pre-source-$(date -u +%Y%m%dT%H%M%SZ).sql"
cat > compose.dev.yaml <<'YAML'
services:
  api:
    environment:
      FILESYSTEM_ONLY_CHECKPOINT: "true"
    volumes:
      - /opt/e2b-runtime/custom/api:/api:ro
  orchestrator:
    environment:
      FILESYSTEM_ONLY_CHECKPOINT: "true"
      ORCHESTRATOR_BINARY: /opt/e2b-runtime/custom/orchestrator
    volumes:
      - /opt/e2b-runtime/custom/orchestrator-launch.sh:/opt/e2b/scripts/orchestrator-launch.sh:ro
  db-migrator:
    volumes:
      - /opt/e2b-runtime/custom/db-migrator:/migrator:ro
YAML
export COMPOSE_FILE=compose.yaml:compose.dev.yaml
# Keep databases/templates and all pinned images; swap in native binaries only.
docker compose stop api orchestrator >>"$log" 2>&1
# Drop stateless containers so repeated installs remount the new binary inodes.
docker compose rm -f api orchestrator db-migrator >>"$log" 2>&1
if ! docker compose run --rm --no-deps db-migrator >>"$log" 2>&1;then echo "Source migration failed; inspect $log" >&2;exit 1;fi
./runtime.sh up
rm -f /tmp/api /tmp/orchestrator /tmp/db-migrator /tmp/runtime.sh /tmp/orchestrator-launch.sh
echo 'Source API/orchestrator installed; filesystem snapshots enabled.'
