#!/usr/bin/env bash
set -euo pipefail
work=$1
rm -f "$work/flow-result.json"
cfg=$(mktemp)
trap 'rm -f "$cfg" "$work/flow-tests" "$work/flow-host.sh" "$work/flow-guest.sh"' EXIT
(cd /home/zyl/e2bruntime/deploy/two-vm && vagrant ssh-config runtime1) > "$cfg"
chmod 600 "$cfg"
scp -q -F "$cfg" "$work/flow-tests" "$work/flow-guest.sh" runtime1:/tmp/
status=0
ssh -F "$cfg" runtime1 'sudo bash /tmp/flow-guest.sh' || status=$?
ssh -F "$cfg" runtime1 'sudo cat /opt/e2b-runtime/flow-result.json' > "$work/flow-result.json"
chmod 600 "$work/flow-result.json"
exit "$status"
