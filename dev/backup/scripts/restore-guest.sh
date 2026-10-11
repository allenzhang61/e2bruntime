#!/usr/bin/env bash
set -euo pipefail
trap 'rm -f /tmp/restore-s3.env /tmp/e2b-backup-tests /tmp/restore-guest.sh' EXIT
set -a
source /opt/e2b-runtime/sdk.env
source /tmp/restore-s3.env
set +a
export BACKUP_NODE=runtime2
export RESTORE_LIVE=1
/tmp/e2b-backup-tests -test.v -test.run '^TestRestoreLatestLive$' -test.timeout=10m
