#!/usr/bin/env bash
set -euo pipefail
work=$1
vm_dir=${RUNTIME_REMOTE_DIR:-/home/zyl/e2bruntime/deploy/two-vm}
s3_env=${RUSTFS_ENV_FILE:-/home/zyl/e2bruntime/dev/deploy/rustfs/.env}
cfg=$(mktemp)
trap 'rm -f "$cfg"' EXIT
for vm in runtime1 runtime2; do
 (cd "$vm_dir" && vagrant ssh-config "$vm") > "$cfg"
 chmod 600 "$cfg"
 scp -q -F "$cfg" "$work/e2b-backup" "$work/run.sh" "$work/service" "$work/config.env.example" "$work/install-guest.sh" "$vm:/tmp/"
 # Key transfer is authorized for this private development S3 integration.
 scp -q -F "$cfg" "$s3_env" "$vm:/tmp/s3.env"
 ssh -F "$cfg" "$vm" "chmod 600 /tmp/s3.env && sudo BACKUP_SEED_DEMO=${BACKUP_SEED_DEMO:-1} bash /tmp/install-guest.sh '$vm'"
done
