#!/usr/bin/env bash
# Explicit destructive rebuild of the two current Runtime development VMs.
set -euo pipefail
[[ ${1:-} == --all-data ]] || { echo 'Usage: reset.sh --all-data (deletes both Runtime VMs, all S3 backups and controller records)'; exit 2; }
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
sandbox=${SI_SANDBOX_DIR:-"$(cd "$here/../../.." && pwd)/si-sandbox"}
[[ -f "$sandbox/src/cmd/control/main.go" ]] || { echo "Set SI_SANDBOX_DIR before resetting the environment" >&2; exit 2; }
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
socket="${TMPDIR:-/tmp}/e2b-logic-control-${UID}.sock"
ssh -S "$socket" -O exit "$remote" >/dev/null 2>&1 || true
ssh "$remote" 'bash -s' <<'HOST'
set -euo pipefail
sudo -n systemctl stop e2b-control-runtime1.service e2b-control-runtime2.service
sudo -n docker rm -f e2b-logic-control
sudo -n docker volume rm e2b-control-data
cd /home/zyl/e2bruntime/dev/deploy/rustfs
sudo -n docker compose down
cd /home/zyl/e2bruntime/deploy/two-vm
vagrant destroy -f runtime1 runtime2
# These are generated source/build outputs, not the local development repository.
sudo -n rm -rf /home/zyl/e2bruntime/dev/build/src /home/zyl/e2bruntime/dev/build/out
mkdir -p /home/zyl/e2bruntime/dev/build/src /home/zyl/e2bruntime/dev/build/out
HOST
"$here/two-vm/deploy.sh" up
"$here/two-vm/build.sh" build
"$here/two-vm/build.sh" install
"$here/rustfs/deploy.sh" up
BACKUP_SEED_DEMO=0 "$here/../backup/scripts/deploy.sh" up
"$here/../web-admin/scripts/web-deploy.sh"
"$sandbox/deploy/control/scripts/deploy.sh"
"$sandbox/deploy/control/scripts/connect.sh"
"$here/two-vm/deploy.sh" verify
RUSTFS_VERIFY_EMPTY=1 "$here/rustfs/deploy.sh" verify
echo 'Rebuild complete; no demo sandboxes created.'
