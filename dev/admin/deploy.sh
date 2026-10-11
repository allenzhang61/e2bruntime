#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=/home/zyl/e2bruntime/dev/admin
bundle=$(mktemp -d)
trap 'rm -rf "$bundle"' EXIT
git clone -q --depth 1 --branch v0.2.1 https://github.com/e2b-dev/dashboard.git "$bundle/source"
[[ $(git -C "$bundle/source" rev-parse HEAD) == f031ba7ec20910d5b09582d6531d7d220cf30c7f ]] || { echo 'Unexpected upstream commit'; exit 1; }
git -C "$bundle/source" apply "$here/dashboard.patch"
(cd "$here/../backup" && GOWORK=off GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$bundle/e2b-backup" .)
cp "$here/../backup/run.sh" "$bundle/admin-run.sh"
cp "$here/../deploy/two-vm/runtime.sh" "$bundle/runtime.sh"
cp "$here/service" "$here/install-host.sh" "$here/install-guest.sh" "$bundle/"
# Remove only the generated upstream checkout so removed routes cannot survive tar overlay.
ssh "$remote" "mkdir -p '$work' && rm -rf '$work/source'"
COPYFILE_DISABLE=1 tar --exclude=.git --no-xattrs -C "$bundle" -cf - . | ssh "$remote" "tar -C '$work' -xf -"
ssh "$remote" "bash '$work/install-host.sh' '$work'"
