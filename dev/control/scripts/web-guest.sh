#!/usr/bin/env bash
set -euo pipefail
node=$1
port=13002
[[ "$node" != runtime2 ]] || port=13012
cd /opt/e2b-runtime
install -d -m 700 backup
if [[ "$node" == runtime2 ]]; then
 install -m 600 /tmp/admin-s3.env backup/s3.env
 if [[ ! -f backup/config.env ]]; then
  printf 'BACKUP_NODE=runtime2\nBACKUP_INTERVAL=2m\nBACKUP_KEEP=10\n' > backup/config.env
 fi
 chmod 600 backup/config.env
 install -m 755 /tmp/admin-run.sh backup/run.sh
fi
install -m 755 /tmp/runtime.sh runtime.sh
install -m 755 /tmp/e2b-backup backup/e2b-backup.next
mv backup/e2b-backup.next backup/e2b-backup
install -m 644 /tmp/service /etc/systemd/system/e2b-backup-admin.service
systemctl daemon-reload
systemctl enable --now e2b-backup-admin.service
systemctl restart e2b-backup-admin.service
# Preserve runtime1's existing periodic backup configuration and runtime2's disabled state.
if systemctl is-active --quiet e2b-backup.service; then systemctl restart e2b-backup.service; fi
# SDK config includes deprecated Dashboard variables; pass only its server key.
(
 source sdk.env
 umask 077
 printf 'E2B_API_KEY=%s\n' "$E2B_API_KEY" > backup/dashboard.env
)
chmod 600 backup/dashboard.env
cat > compose.dashboard.yaml <<YAML
services:
  dashboard:
    image: e2b-dashboard-local:snapshots-v1
    pull_policy: never
    env_file:
      - ./backup/dashboard.env
    environment:
      HOSTNAME: 127.0.0.1
      PUBLIC_SANDBOX_URL: http://127.0.0.1:$port
      E2B_BACKUP_API_URL: http://127.0.0.1:3012
YAML
files=compose.yaml
[[ ! -f compose.dev.yaml ]] || files=$files:compose.dev.yaml
export COMPOSE_FILE=$files:compose.dashboard.yaml
docker compose up -d --no-deps dashboard
for i in $(seq 1 60); do
 if curl -fsS --max-time 2 http://127.0.0.1:3001/api/health >/dev/null 2>&1; then echo 'Dashboard healthy'; break; fi
 sleep 1
done
curl -fsS --max-time 5 http://127.0.0.1:3001/api/health >/dev/null
systemctl is-active e2b-backup-admin.service
rm -f /tmp/runtime.sh /tmp/e2b-backup /tmp/service /tmp/install-guest.sh /tmp/admin-s3.env /tmp/admin-run.sh
