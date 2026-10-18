#!/usr/bin/env bash
set -euo pipefail
export GOWORK=off GOFLAGS=-buildvcs=false
component=${1:-all}
if [[ $component == all ]]; then
  bash /src/dev/deploy/two-vm/compile.sh
  pushd /src/dev/backup >/dev/null
  go test -short ./src
  CGO_ENABLED=0 go build -o /out/e2b-backup ./src
  popd >/dev/null
  pushd /src/dev/deploy/rustfs >/dev/null
  CGO_ENABLED=0 go test -c -o /out/s3-check .
  popd >/dev/null
fi
pushd /src/dev/control >/dev/null
go test -short ./...
CGO_ENABLED=0 go build -o /out/e2b-logic-control ./src
