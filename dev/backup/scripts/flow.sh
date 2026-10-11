#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/.." && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=/home/zyl/e2bruntime/dev/backup
build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
(cd "$root" && GOWORK=off GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o "$build/flow-tests" ./src)
ssh "$remote" "mkdir -p '$work'"
scp -q "$build/flow-tests" "$here/flow-host.sh" "$here/flow-guest.sh" "$remote:$work/"
mkdir -p "$root/out"
rm -f "$root/out/flow-result.json"
status=0
ssh "$remote" "bash '$work/flow-host.sh' '$work'" || status=$?
mkdir -p "$root/out"
scp -q "$remote:$work/flow-result.json" "$root/out/flow-result.json" || status=1
exit "$status"
