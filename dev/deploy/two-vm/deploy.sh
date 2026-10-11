#!/usr/bin/env bash
# Upload a minimal bundle; keep Vagrant state on the remote Linux host.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
remote_dir=${RUNTIME_REMOTE_DIR:-/home/zyl/e2bruntime/deploy/two-vm}
action=${1:-up}
[[ $remote != -* && $remote =~ ^[a-zA-Z0-9_.@:-]+$ ]] || { echo 'Invalid SSH host' >&2; exit 2; }
[[ $remote_dir =~ ^/[a-zA-Z0-9_./-]+$ ]] || { echo 'Remote path must be absolute without spaces' >&2; exit 2; }
case "$action" in
  up|provision)
    bundle=$(mktemp -d)
    trap 'rm -rf "$bundle"' EXIT
    mkdir "$bundle/payload"
    cp "$here/"{Vagrantfile,provision.sh,runtime.sh} "$bundle/"
    cp "$repo/embed/compose/compose.yaml" "$bundle/payload/compose.yaml"
    cp "$repo/embed/compose/.env" "$bundle/payload/compose.env"
    ssh "$remote" "mkdir -p '$remote_dir'"
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$bundle" -cf - . | ssh "$remote" "tar -C '$remote_dir' -xf -"
    if [[ $action == provision ]]; then
      ssh "$remote" "cd '$remote_dir' && vagrant validate && vagrant provision"
    else
      ssh "$remote" "cd '$remote_dir' && vagrant validate && vagrant up --provider=libvirt"
    fi
    ;;
  status|halt)
    ssh "$remote" "cd '$remote_dir' && vagrant '$action'"
    ;;
  verify|smoke|runtime-up)
    guest_action=$action
    [[ $action != runtime-up ]] || guest_action=up
    for vm in runtime1 runtime2; do
      ssh "$remote" "cd '$remote_dir' && vagrant ssh '$vm' -c 'sudo /opt/e2b-runtime/runtime.sh $guest_action'"
    done
    ;;
  *) echo 'Usage: ./deploy.sh {up|provision|status|verify|smoke|runtime-up|halt}' >&2; exit 2 ;;
esac
