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
vm_dir=$2
cfg=$(mktemp)
trap 'rm -f "$cfg"' EXIT
for vm in runtime1 runtime2; do
 (cd "$vm_dir" && vagrant ssh-config "$vm") > "$cfg"
 chmod 600 "$cfg"
 scp -q -F "$cfg" "$work/out/api" "$work/out/orchestrator" "$work/out/db-migrator" "$work/runtime.sh" "$work/orchestrator-launch.sh" "$work/install-vm.sh" "$vm:/tmp/"
 # All commands execute inside the guest. Full output may contain generated secrets;
 # keep it in a root-only log rather than echoing it to the deployment console.
 ssh -F "$cfg" "$vm" 'sudo bash /tmp/install-vm.sh'
done
