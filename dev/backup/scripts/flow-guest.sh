#!/usr/bin/env bash
set -euo pipefail
trap 'rm -f /tmp/flow-tests /tmp/flow-guest.sh' EXIT
rm -f /opt/e2b-runtime/flow-result.json
systemctl is-active --quiet e2b-backup.service || { echo 'runtime1 backup service must be active'; exit 1; }
set -a
source /opt/e2b-runtime/sdk.env
source /opt/e2b-runtime/backup/s3.env
source /opt/e2b-runtime/backup/config.env
set +a
export FLOW_LIVE=1 FLOW_WAIT=3m FLOW_REPORT=/opt/e2b-runtime/flow-result.json
/tmp/flow-tests -test.v -test.run '^TestSandboxFlowLive$' -test.timeout=10m
