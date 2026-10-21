#!/usr/bin/env bash
set -euo pipefail
work=$1
# These installers share guest staging paths and mutate the same Runtime services.
# Fail before uploading to guests so concurrent deployments cannot replace inputs.
exec 9>/home/zyl/e2bruntime/deploy/.runtime-deploy.lock
if ! flock -n 9; then
 echo 'Another Runtime deployment is running (source, backup or Web Admin). Retry after it finishes.' >&2
 exit 1
fi
sudo -n docker build --memory 6g -t e2b-dashboard-local:snapshots-v1 "$work/source"
cfg=$(mktemp)
trap 'rm -f "$cfg"' EXIT
for node in runtime1 runtime2; do
 (cd /home/zyl/e2bruntime/deploy/two-vm && vagrant ssh-config "$node") > "$cfg"
 chmod 600 "$cfg"
 sudo -n docker save e2b-dashboard-local:snapshots-v1 | ssh -F "$cfg" "$node" 'sudo docker load'
 scp -q -F "$cfg" "$work/e2b-backup" "$work/runtime.sh" "$work/service" "$work/install-guest.sh" "$work/admin-run.sh" "$node":/tmp/
 if [[ "$node" == runtime2 ]]; then
  scp -q -F "$cfg" /home/zyl/e2bruntime/dev/deploy/rustfs/.env "$node":/tmp/admin-s3.env
  ssh -F "$cfg" "$node" 'chmod 600 /tmp/admin-s3.env'
 fi
 ssh -F "$cfg" "$node" "sudo bash /tmp/install-guest.sh '$node'"
done
