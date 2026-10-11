#!/usr/bin/env bash
# The fresh guest is prepared separately; this does not touch runtime1.
set -euo pipefail
work=$1
vm_dir=/home/zyl/e2bruntime/deploy/two-vm
build=/home/zyl/e2bruntime/dev/build
cfg=$(mktemp)
trap 'rm -f "$cfg"' EXIT
(cd "$vm_dir" && vagrant ssh-config runtime2) > "$cfg"
chmod 600 "$cfg"
scp -q -F "$cfg" "$build/out/api" "$build/out/orchestrator" "$build/out/db-migrator" "$build/runtime.sh" "$build/orchestrator-launch.sh" "$build/install-vm.sh" runtime2:/tmp/
ssh -F "$cfg" runtime2 'sudo bash /tmp/install-vm.sh'
scp -q -F "$cfg" "$work/e2b-backup-tests" "$work/restore-guest.sh" runtime2:/tmp/
scp -q -F "$cfg" /home/zyl/e2bruntime/dev/deploy/rustfs/.env runtime2:/tmp/restore-s3.env
ssh -F "$cfg" runtime2 'chmod 600 /tmp/restore-s3.env && sudo bash /tmp/restore-guest.sh'
ssh -F "$cfg" runtime2 'sudo cat /opt/e2b-runtime/restore-result.json' > "$work/restore-result.json"
chmod 600 "$work/restore-result.json"
cat "$work/restore-result.json"
