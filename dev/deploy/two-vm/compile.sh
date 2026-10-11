#!/usr/bin/env bash
set -euo pipefail
export GOWORK=off
cd /src/packages/shared
# Existing flag tests verify unset/true/false/invalid environment values.
go test ./pkg/featureflags -run '^TestEnvBoolOr$' -count=1
cd /src/packages/api
make build VERSION=0.14.202610100001-dev
cp bin/api /out/api
cd /src/packages/db
CGO_ENABLED=0 go build -o /out/db-migrator ./scripts/migrator.go
cd /src/packages/orchestrator
make build-local VERSION=0.16.202610100001-dev
cp bin/orchestrator /out/orchestrator
go test ./pkg/server -run 'TestCheckpoint_FilesystemOnly|TestCheckpoint_FilesystemOnlyRefusedWithoutItsFlag' -count=1
chmod 755 /out/api /out/db-migrator /out/orchestrator
