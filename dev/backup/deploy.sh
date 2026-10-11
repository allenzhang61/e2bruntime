#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=/home/zyl/e2bruntime/dev/backup
case "${1:-up}" in
 up)
  (cd "$here" && GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/e2b-backup .)
  ssh "$remote" "mkdir -p '$work'"
  scp -q /tmp/e2b-backup "$here/run.sh" "$here/service" "$here/config.env.example" "$here/install-guest.sh" "$here/install-host.sh" "$remote:$work/"
  ssh "$remote" "BACKUP_SEED_DEMO=${BACKUP_SEED_DEMO:-1} bash '$work/install-host.sh' '$work'"
  ;;
 status|logs|stop)
  for vm in runtime1 runtime2; do
   case "$1" in
    status) command='sudo systemctl is-active e2b-backup.service && sudo /opt/e2b-runtime/backup/run.sh status';;
    logs) command='sudo journalctl -u e2b-backup.service -n 12 --no-pager';;
    stop) command='sudo systemctl disable --now e2b-backup.service';;
   esac
   ssh "$remote" "cd /home/zyl/e2bruntime/deploy/two-vm && vagrant ssh '$vm' -c '$command'"
  done
  ;;
 *) echo 'Usage: deploy.sh {up|status|logs|stop}' >&2;exit 2;;
esac
