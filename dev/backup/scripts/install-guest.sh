#!/usr/bin/env bash
set -euo pipefail
vm=$1
[[ $vm == runtime1 || $vm == runtime2 ]]
systemctl stop e2b-backup.service 2>/dev/null || true
install -d -m 700 /opt/e2b-runtime/backup
install -m 755 /tmp/e2b-backup /opt/e2b-runtime/backup/
install -m 755 /tmp/run.sh /opt/e2b-runtime/backup/
install -m 600 /tmp/s3.env /opt/e2b-runtime/backup/
install -m 600 /tmp/config.env.example /opt/e2b-runtime/backup/config.env
sed -i "s/BACKUP_NODE=runtime1/BACKUP_NODE=$vm/" /opt/e2b-runtime/backup/config.env
install -m 644 /tmp/service /etc/systemd/system/e2b-backup.service
rm -f /tmp/s3.env /tmp/e2b-backup /tmp/run.sh /tmp/config.env.example /tmp/service
if [[ ${BACKUP_SEED_DEMO:-1} == 1 ]]; then /opt/e2b-runtime/backup/run.sh seed; fi
systemctl daemon-reload
systemctl enable --now e2b-backup.service
