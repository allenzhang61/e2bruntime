#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
control=$(cd "$here/.." && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=/home/zyl/e2bruntime/dev/control
bundle=$(mktemp -d)
trap 'rm -rf "$bundle"' EXIT
(cd "$control" && GOWORK=off GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$bundle/e2b-logic-control" ./src)
cp "$control/Dockerfile" "$control/.dockerignore" "$bundle/"
mkdir "$bundle/scripts"
cp "$here/install-host.sh" "$bundle/scripts/"
ssh "$remote" "mkdir -p '$work'"
COPYFILE_DISABLE=1 tar --no-xattrs -C "$bundle" -cf - . | ssh "$remote" "tar -C '$work' -xf -"
ssh "$remote" "bash '$work/scripts/install-host.sh' '$work'"
