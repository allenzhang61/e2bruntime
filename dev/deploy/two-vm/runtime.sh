#!/usr/bin/env bash
set -euo pipefail
cd /opt/e2b-runtime
files=compose.yaml
[[ ! -f compose.dev.yaml ]] || files=$files:compose.dev.yaml
[[ ! -f compose.dashboard.yaml ]] || files=$files:compose.dashboard.yaml
export COMPOSE_FILE=$files
[[ $EUID -eq 0 ]] || { echo 'Run with sudo inside the VM.' >&2; exit 1; }
export HUGEPAGES=2048
case "${1:-verify}" in
  up|smoke)
    action=$1
    log="/var/log/e2b-runtime-${action}.log"
    touch "$log"
    chmod 600 "$log"
    echo "Runtime ${action}: detailed output saved to ${log}."
    if [[ $action == up ]]; then
      docker compose config --quiet
      if ! docker compose up -d --wait --wait-timeout 1200 >>"$log" 2>&1; then
        echo "Runtime start failed; inspect ${log} inside the VM." >&2
        docker compose ps -a
        exit 1
      fi
      # ready logs include the team API key. Never emit them to deployment output.
      umask 077
      docker compose exec -T ready cat /run/e2b/sdk.env > sdk.env.tmp
      mv sdk.env.tmp sdk.env
    else
      if ! docker compose --profile test run --rm smoke >>"$log" 2>&1; then
        echo "Smoke test failed; inspect ${log} inside the VM." >&2
        exit 1
      fi
      echo 'Smoke passed: sandbox creation, command execution, proxy access and destruction.'
    fi
    ;;
  verify) ;;
  *) echo 'Usage: sudo ./runtime.sh {up|verify|smoke}' >&2; exit 2 ;;
esac
curl -fsS --max-time 10 http://127.0.0.1:3000/health >/dev/null
curl -fsS --max-time 10 http://127.0.0.1:5008/health >/dev/null
curl -fsS --max-time 10 http://127.0.0.1:3003/ >/dev/null
echo 'API, orchestrator and client-proxy health checks passed.'
docker compose ps
