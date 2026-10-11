#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$here"
remote=${RUNTIME_SSH_HOST:-zyl@192.168.1.211}
remote_dir=${RUSTFS_REMOTE_DIR:-/home/zyl/e2bruntime/dev/deploy/rustfs}
[[ $remote != -* && $remote =~ ^[a-zA-Z0-9_.@:-]+$ ]] || exit 2
[[ $remote_dir =~ ^/[a-zA-Z0-9_./-]+$ ]] || exit 2
case "${1:-up}" in
  up)
    if [[ ! -e .env ]]; then
      umask 077
      # Generated here once; never display credentials in deployment output.
      {
        echo 'RUSTFS_IMAGE=rustfs/rustfs:latest'
        echo 'RUSTFS_BIND_IP=192.168.1.211'
        echo 'RUSTFS_ENDPOINT=http://192.168.1.211:19000'
        printf 'RUSTFS_ACCESS_KEY=E2B%s\n' "$(openssl rand -hex 10 | tr 'a-f' 'A-F')"
        printf 'RUSTFS_SECRET_KEY=%s\n' "$(openssl rand -hex 24)"
        echo 'RUSTFS_BUCKET_NAME=runtime-snapshots'
        echo 'RUSTFS_REGION=us-east-1'
      } > .env
    fi
    chmod 600 .env
    ssh "$remote" "umask 077; mkdir -p '$remote_dir'; chmod 700 '$remote_dir'"
    COPYFILE_DISABLE=1 tar --no-xattrs -cf - compose.yaml .env | ssh "$remote" "tar -C '$remote_dir' -xf -; chmod 600 '$remote_dir/.env'"
    ssh "$remote" "cd '$remote_dir' && sudo -n docker compose config --quiet && sudo -n docker compose up -d"
    # Pin the resolved image digest for repeat deployments, without echoing secrets.
    digest=$(ssh "$remote" "cd '$remote_dir' && sudo -n docker compose images -q rustfs | xargs -r sudo -n docker image inspect --format '{{index .RepoDigests 0}}'")
    if [[ $digest == rustfs/rustfs@sha256:* ]]; then
      tmp=$(mktemp)
      sed "s|^RUSTFS_IMAGE=.*|RUSTFS_IMAGE=$digest|" .env > "$tmp"
      cat "$tmp" > .env
      rm -f "$tmp"
      scp -q .env "$remote:$remote_dir/.env"
    fi
    set -a
    source .env
    set +a
    for attempt in {1..60}; do
      if curl -fsS --max-time 2 "$RUSTFS_ENDPOINT/health" >/dev/null 2>&1; then break; fi
      sleep 2
    done
    curl -fsS --max-time 5 "$RUSTFS_ENDPOINT/health" >/dev/null
    GOWORK=off go test -v -count=1 -timeout=3m
    ;;
  verify)
    set -a; source .env; set +a
    GOWORK=off go test -v -count=1 -timeout=3m
    ;;
  status)
    ssh "$remote" "cd '$remote_dir' && sudo -n docker compose ps"
    ;;
  down)
    # Only this dedicated Compose project is removed; no global Docker prune.
    ssh "$remote" "cd '$remote_dir' && sudo -n docker compose down"
    ;;
  *) echo 'Usage: ./deploy.sh {up|verify|status|down}' >&2; exit 2 ;;
esac
