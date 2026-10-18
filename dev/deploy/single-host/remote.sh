#!/usr/bin/env bash
# Host-side Git bootstrap. The checkout and root-owned build outputs are disposable.
set -euo pipefail
umask 077
: "${E2BRUNTIME_CONFIG:?}"
source "$E2BRUNTIME_CONFIG"
: "${E2BRUNTIME_WORK:?}" "${E2BRUNTIME_REPO:?}"
ref=${E2BRUNTIME_REF:-HEAD}
[[ $ref != -* && $ref =~ ^[a-zA-Z0-9_./-]+$ ]] || exit 2
mkdir -p "$E2BRUNTIME_WORK"
checkout=$(mktemp -d "$E2BRUNTIME_WORK/source.XXXXXXXX")
trap 'sudo -n rm -rf -- "$checkout"' EXIT
export GIT_SSH_COMMAND='ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15'
git init -q "$checkout"
git -C "$checkout" fetch --depth=1 "$E2BRUNTIME_REPO" "$ref"
git -C "$checkout" checkout -q --detach FETCH_HEAD
bash "$checkout/dev/deploy/single-host/deploy.sh" "$@"
