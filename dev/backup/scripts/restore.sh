#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/.." && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=/home/zyl/e2bruntime/dev/backup
(cd "$root" && GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/e2b-backup-tests ./src)
scp -q /tmp/e2b-backup-tests "$here/restore-host.sh" "$here/restore-guest.sh" "$remote:$work/"
ssh "$remote" "bash '$work/restore-host.sh' '$work'"
mkdir -p "$root/out"
scp -q "$remote:$work/restore-result.json" "$root/out/runtime2-restore.json"
