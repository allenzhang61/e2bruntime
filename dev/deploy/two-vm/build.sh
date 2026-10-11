#!/usr/bin/env bash
# Build this fork on the native Linux host, then install only on the new Runtime VMs.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
work=${RUNTIME_BUILD_DIR:-/home/zyl/e2bruntime/dev/build}
vm_dir=${RUNTIME_REMOTE_DIR:-/home/zyl/e2bruntime/deploy/two-vm}
[[ $remote != -* && $remote =~ ^[a-zA-Z0-9_.@:-]+$ ]] || exit 2
[[ $work =~ ^/[a-zA-Z0-9_./-]+$ && $vm_dir =~ ^/[a-zA-Z0-9_./-]+$ ]] || exit 2
case "${1:-build}" in
 build)
  ssh "$remote" "mkdir -p '$work/src' '$work/out'"
  COPYFILE_DISABLE=1 tar --no-xattrs --exclude='.env*' --exclude=bin --exclude=.git -C "$repo" -cf - packages go.work go.work.sum |
    ssh "$remote" "tar -C '$work/src' -xf -"
  scp -q "$here/compile.sh" "$remote:$work/compile.sh"
  ssh "$remote" "sudo -n docker run --rm --memory 4g --cpus 4 -e GOMAXPROCS=2 -e GOGC=60 -e GOMEMLIMIT=3GiB -v '$work/src:/src' -v '$work/out:/out' -v '$work/compile.sh:/compile.sh:ro' -v e2bruntime-go-mod:/go/pkg/mod -v e2bruntime-go-cache:/root/.cache/go-build -w /src golang:1.26.8-bookworm bash /compile.sh"
  ;;
 install)
  scp -q "$here/install.sh" "$here/install-vm.sh" "$here/runtime.sh" "$repo/embed/compose/scripts/orchestrator-launch.sh" "$remote:$work/"
  ssh "$remote" "bash '$work/install.sh' '$work' '$vm_dir'"
  ;;
 rollback)
  for vm in runtime1 runtime2; do
   ssh "$remote" "cd '$vm_dir' && vagrant ssh '$vm' -c 'sudo mv /opt/e2b-runtime/compose.dev.yaml /opt/e2b-runtime/compose.dev.disabled && sudo /opt/e2b-runtime/runtime.sh up'"
  done
  ;;
 *) echo 'Usage: ./build.sh {build|install|rollback}' >&2; exit 2 ;;
esac
