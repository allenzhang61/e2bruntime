#!/usr/bin/env bash
set -euo pipefail
set -a
source /opt/e2b-runtime/sdk.env
source /opt/e2b-runtime/backup/s3.env
source /opt/e2b-runtime/backup/config.env
set +a
exec /opt/e2b-runtime/backup/e2b-backup "${@}"
