#!/usr/bin/env bash
set -euo pipefail
export GOWORK=off
: "${COMMIT_SHA:?build.sh must supply the source commit}"
[[ $COMMIT_SHA =~ ^[0-9a-f]+$ ]] || exit 2
cd /src/packages/shared
# Existing flag tests verify unset/true/false/invalid environment values.
go test ./pkg/featureflags -run '^TestEnvBoolOr$' -count=1
cd /src/packages/api
# The uploaded source bundle intentionally has no .git or environment files.
# Build directly rather than invoking Makefile environment/Git discovery.
expected_migration=$(printf '%s\n' ../db/migrations/*.sql | sed 's|.*/||; s/_.*//' | sort | tail -n 1)
[[ $expected_migration =~ ^[0-9]+$ ]] || { echo 'No migration timestamp found' >&2; exit 1; }
CGO_ENABLED=0 go build -o bin/api -ldflags "-X=main.commitSHA=$COMMIT_SHA -X=main.expectedMigrationTimestamp=$expected_migration -X=main.serviceVersion=0.14.202610100001-dev" .
cp bin/api /out/api
cd /src/packages/db
CGO_ENABLED=0 go build -o /out/db-migrator ./scripts/migrator.go
cd /src/packages/orchestrator
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o bin/orchestrator -ldflags "-X=main.commitSHA=$COMMIT_SHA -X=github.com/e2b-dev/infra/packages/orchestrator/pkg/version.Version=0.16.202610100001-dev" .
cp bin/orchestrator /out/orchestrator
go test ./pkg/server -run 'TestCheckpoint_FilesystemOnly|TestCheckpoint_FilesystemOnlyRefusedWithoutItsFlag' -count=1
chmod 755 /out/api /out/db-migrator /out/orchestrator
