#!/usr/bin/env bash
set -euo pipefail
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
socket="${TMPDIR:-/tmp}/e2b-logic-control-${UID}.sock"
if ssh -S "$socket" -O check "$remote" >/dev/null 2>&1; then echo 'SSH forward already running'; exit 0; fi
# Resolve current Vagrant addresses; recreated guests may receive different IPs.
proxy1=${RUNTIME1_PROXY_HOST:-$(ssh "$remote" "awk '/HostName/{print \$2; exit}' /home/zyl/e2bruntime/dev/control/runtime1.ssh")}
proxy2=${RUNTIME2_PROXY_HOST:-$(ssh "$remote" "awk '/HostName/{print \$2; exit}' /home/zyl/e2bruntime/dev/control/runtime2.ssh")}
[[ $proxy1 =~ ^[0-9.]+$ && $proxy2 =~ ^[0-9.]+$ ]] || { echo 'Invalid Runtime proxy address'; exit 1; }
ssh -fMNT -S "$socket" -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
 -L 127.0.0.1:13021:127.0.0.1:13021 \
 -L 127.0.0.1:13001:127.0.0.1:14001 \
 -L 127.0.0.1:13011:127.0.0.1:14011 \
 -L 127.0.0.1:13002:${proxy1}:3002 \
 -L 127.0.0.1:13012:${proxy2}:3002 "$remote"
echo 'Logic Control: http://localhost:13021'
