#!/usr/bin/env bash
# Run on an x86_64 Linux host from a checkout of the desired Git revision.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_root=$(git -C "$here" rev-parse --show-toplevel)
action=${1:-help}
if [[ $action == help ]]; then
  echo "Usage: bash dev/deploy/single-host/deploy.sh {build|control-build|deploy|control-deploy|status|verify|smoke|rebuild --delete-runtime-data}"
  echo "Set E2BRUNTIME_CONFIG to a host-side shell configuration file."
  exit
fi
[[ -f ${E2BRUNTIME_CONFIG:-} ]] || { echo "E2BRUNTIME_CONFIG required" >&2; exit 2; }
source "$E2BRUNTIME_CONFIG"
: "${E2BRUNTIME_WORK:?}" "${CONTROL_LISTEN:?}" "${CONTROL_URL:?}"
if [[ $action == rebuild && ${2:-} != --delete-runtime-data ]]; then
  echo "rebuild deletes Runtime databases, templates and sandboxes; pass --delete-runtime-data." >&2
  exit 2
fi
runtime=/opt/e2b-runtime
control=/opt/e2b-control
base=$E2BRUNTIME_WORK
mkdir -p "$base"
exec 9>"$base/deployment.lock"
flock -n 9 || { echo "Another single-host deployment is running." >&2; exit 1; }
sudo -n true
compose() {
  sudo -n docker compose -p e2b --project-directory "$runtime" \
    -f "$runtime/compose.yaml" -f "$runtime/compose.dev.yaml" "$@"
}
verify() {
  sudo -n bash "$runtime/runtime.sh" verify
  curl -fsS --max-time 10 "$CONTROL_URL/health"
  curl -fsS --max-time 45 "$CONTROL_URL/api/logics" > "$base/inventory.json"
  sudo -n docker run --rm --network none -v "$base/inventory.json:/inventory.json:ro" \
    golang:1.26.8-bookworm bash -ec \
    'test "$(grep -o "\"name\":\"runtime[0-9]*\"" /inventory.json | wc -l)" -eq 1; grep -q "\"runtime_errors\":{}" /inventory.json'
  curl -fsS --max-time 15 "$CONTROL_URL/api/backups"
  sudo -n systemctl is-active e2b-backup.service e2b-backup-admin.service
}
case "$action" in
  status)
    compose ps -a
    sudo -n docker ps --filter name=e2b-logic-control --filter name=e2b-web-admin
    sudo -n systemctl status e2b-backup.service e2b-backup-admin.service --no-pager
    exit ;;
  verify) verify; exit ;;
  smoke) sudo -n bash "$runtime/runtime.sh" smoke; exit ;;
  build|control-build|deploy|control-deploy|rebuild) ;;
  *) echo "Unknown action: $action" >&2; exit 2 ;;
esac
test "$(uname -m)" = x86_64
sudo -n docker compose version
if [[ $action == deploy || $action == rebuild ]]; then
  test -c /dev/kvm
  test -c /dev/net/tun
  [[ -z ${REQUIRED_MOUNT:-} ]] || mountpoint -q "$REQUIRED_MOUNT"
  test -f "${S3_ENV:?}"
fi
component=all
[[ $action != control-* ]] || component=control
stage=$(mktemp -d "$base/build.XXXXXXXX")
trap 'sudo -n rm -rf -- "$stage"' EXIT
mkdir -p "$stage/out"
commit=$(git -C "$source_root" rev-parse HEAD)
# Only the disposable checkout is writable; host Git credentials never enter Docker.
sudo -n docker run --rm --memory 4g --cpus 4 \
  -e COMMIT_SHA="$commit" -e GOMAXPROCS=2 -e GOGC=60 -e GOMEMLIMIT=3GiB \
  -v "$source_root:/src" -v "$stage/out:/out" \
  -v e2bruntime-go-mod:/go/pkg/mod -v e2bruntime-go-cache:/root/.cache/go-build \
  -w /src golang:1.26.8-bookworm bash /src/dev/deploy/single-host/build.sh "$component"
mkdir -p "$base/out"
sudo -n cp "$stage/out/"* "$base/out/"
printf '%s\n' "$commit" > "$base/commit"
if [[ $action == build || $action == control-build ]]; then
  echo "Build complete at $commit. Artifacts: $base/out"
  exit
fi
mkdir -p "$stage/control-image"
cp "$stage/out/e2b-logic-control" "$source_root/dev/control/Dockerfile" "$stage/control-image/"
sudo -n docker build -t e2b-logic-control:single "$stage/control-image"

install_web() {
  : "${WEB_ADMIN_LISTEN:?}" "${WEB_ADMIN_ORIGINS:?}" "${PUBLIC_SANDBOX_URL:?}"
  # Values are substituted into Nginx syntax; refuse directives and newlines.
  [[ $WEB_ADMIN_LISTEN =~ ^[a-zA-Z0-9.-]+:[0-9]+$ ]] || return 2
  local origin
  for origin in $WEB_ADMIN_ORIGINS; do
    [[ $origin =~ ^https?://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]] || return 2
  done
  [[ $PUBLIC_SANDBOX_URL =~ ^https?://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]] || return 2
  sudo -n docker run --rm --network none \
    -e "WEB_ADMIN_LISTEN=$WEB_ADMIN_LISTEN" -e "WEB_ADMIN_ORIGINS=$WEB_ADMIN_ORIGINS" \
    -v "$source_root/dev/control/assets/web-admin.conf.template:/template:ro" \
    nginx:1.28.0-alpine sh -ec \
    'envsubst '\''${WEB_ADMIN_LISTEN} ${WEB_ADMIN_ORIGINS}'\'' < /template' > "$stage/web-admin.conf"
  # nginx -t opens listeners; the isolated container cannot bind the host's LAN IP.
  sed "s/listen $WEB_ADMIN_LISTEN;/listen 127.0.0.1:13001;/" \
    "$stage/web-admin.conf" > "$stage/web-admin-test.conf"
  sudo -n docker run --rm --network none \
    -v "$stage/web-admin-test.conf:/etc/nginx/nginx.conf:ro" nginx:1.28.0-alpine nginx -t
  sudo -n install -m 644 "$stage/web-admin.conf" "$runtime/web-admin.conf"
  cp "$here/compose.override.yaml" "$stage/compose.dev.yaml"
  # Persist interpolation input because later system/root calls do not inherit this shell.
  printf '\nPUBLIC_SANDBOX_URL=%s\n' "$PUBLIC_SANDBOX_URL" >> "$stage/compose.env"
  if [[ ${WEB_ADMIN_ENABLED:-1} == 1 ]]; then
    cat <<'COMPOSE' >> "$stage/compose.dev.yaml"
  web-admin:
    image: nginx:1.28.0-alpine
    restart: unless-stopped
    network_mode: host
    volumes:
      - /opt/e2b-runtime/web-admin.conf:/etc/nginx/nginx.conf:ro
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
COMPOSE
  fi
  sudo -n install -m 600 "$stage/compose.env" "$runtime/.env"
  sudo -n install -m 600 "$stage/compose.dev.yaml" "$runtime/compose.dev.yaml"
}
if [[ $action == control-deploy ]]; then
  sudo -n cat "$runtime/.env" > "$stage/compose.env"
  install_web
  services=(dashboard)
  [[ ${WEB_ADMIN_ENABLED:-1} != 1 ]] || services+=(web-admin)
  if [[ ${WEB_ADMIN_ENABLED:-1} == 0 ]] && sudo -n docker container inspect e2b-web-admin-1 >/dev/null 2>&1; then
    sudo -n docker rm -f e2b-web-admin-1 >/dev/null
  fi
  # Recreate the proxy so replacing its bind-mounted config inode takes effect.
  compose up -d --no-deps --force-recreate "${services[@]}"
else
  endpoint=${RUSTFS_ENDPOINT:?}
  bucket=${RUSTFS_BUCKET_NAME:?}
  region=${RUSTFS_REGION:-us-east-1}
  set -a
  source "$S3_ENV"
  set +a
  RUSTFS_ENDPOINT=$endpoint RUSTFS_BUCKET_NAME=$bucket RUSTFS_REGION=$region
  : "${RUSTFS_ACCESS_KEY:?}" "${RUSTFS_SECRET_KEY:?}" "${RUSTFS_ENDPOINT:?}" "${RUSTFS_BUCKET_NAME:?}"
  export RUSTFS_ENDPOINT RUSTFS_BUCKET_NAME RUSTFS_REGION
  "$stage/out/s3-check" -test.run '^TestS3$' -test.v
  cp "$source_root/embed/compose/.env" "$stage/compose.env"
  # Preserve pinned credentials and operator settings on a non-destructive update.
  if [[ $action != rebuild ]] && sudo -n test -f "$runtime/.env"; then
    sudo -n cat "$runtime/.env" > "$stage/compose.env"
  fi
  if [[ $action == rebuild ]]; then
    echo "Deleting only the e2b host Runtime data; S3 and Control data remain."
    sudo -n systemctl stop e2b-backup.service e2b-backup-admin.service 2>/dev/null || true
    compose down -v --remove-orphans
    sudo -n docker run --rm --privileged --network host --pid host --cgroupns host \
      --entrypoint /bin/sh \
      "$(sed -n 's/^E2B_TOOLS_IMAGE=\([^ ]*\).*/\1/p' "$stage/compose.env")" \
      -c 'exec nsenter -t 1 -m -u -i -n -p -C -- /bin/bash -s < /opt/e2b/scripts/host-teardown.sh'
    if sudo -n test -d "$runtime/backup"; then
      sudo -n mv "$runtime/backup" "$runtime/backup.previous-$(date -u +%Y%m%dT%H%M%SZ)"
    fi
  fi
  sudo -n install -d -m 700 "$runtime" "$runtime/custom" "$runtime/backup" "$control"
  sudo -n install -m 600 "$source_root/embed/compose/compose.yaml" "$runtime/compose.yaml"
  sudo -n install -m 755 "$source_root/dev/deploy/two-vm/runtime.sh" "$runtime/runtime.sh"
  sudo -n install -m 755 "$source_root/embed/compose/scripts/orchestrator-launch.sh" "$runtime/custom/"
  install_web
  for binary in api orchestrator db-migrator; do
    sudo -n install -m 755 "$stage/out/$binary" "$runtime/custom/$binary.next"
    sudo -n mv "$runtime/custom/$binary.next" "$runtime/custom/$binary"
  done
  compose stop api orchestrator
  compose rm -f api orchestrator db-migrator
  if [[ ${WEB_ADMIN_ENABLED:-1} == 1 ]]; then
    compose up -d --no-deps --force-recreate web-admin
  elif sudo -n docker container inspect e2b-web-admin-1 >/dev/null 2>&1; then
    sudo -n docker rm -f e2b-web-admin-1 >/dev/null
  fi
  sudo -n bash "$runtime/runtime.sh" up
  printf 'RUSTFS_ENDPOINT=%q\nRUSTFS_ACCESS_KEY=%q\nRUSTFS_SECRET_KEY=%q\nRUSTFS_BUCKET_NAME=%q\nRUSTFS_REGION=%q\n' \
    "$RUSTFS_ENDPOINT" "$RUSTFS_ACCESS_KEY" "$RUSTFS_SECRET_KEY" "$RUSTFS_BUCKET_NAME" "$RUSTFS_REGION" > "$stage/s3.env"
  sudo -n install -m 600 "$stage/s3.env" "$runtime/backup/s3.env"
  sudo -n install -m 755 "$stage/out/e2b-backup" "$runtime/backup/e2b-backup.next"
  sudo -n mv "$runtime/backup/e2b-backup.next" "$runtime/backup/e2b-backup"
  sudo -n install -m 755 "$source_root/dev/backup/scripts/run.sh" "$runtime/backup/run.sh"
  if ! sudo -n test -f "$runtime/backup/config.env"; then
    sed 's/BACKUP_IMPORT_SOURCES=runtime1,runtime2/BACKUP_IMPORT_SOURCES=runtime1/' \
      "$source_root/dev/backup/config.env.example" > "$stage/backup.env"
    sudo -n install -m 600 "$stage/backup.env" "$runtime/backup/config.env"
  fi
  sudo -n install -m 644 "$source_root/dev/backup/service" /etc/systemd/system/e2b-backup.service
  sudo -n install -m 644 "$source_root/dev/control/assets/web/service" /etc/systemd/system/e2b-backup-admin.service
  sudo -n systemctl daemon-reload
  sudo -n systemctl enable e2b-backup.service e2b-backup-admin.service
  sudo -n systemctl restart e2b-backup.service e2b-backup-admin.service
fi
sudo -n bash -s > "$stage/control.env" <<'ENV'
set -euo pipefail
source /opt/e2b-runtime/sdk.env
source /opt/e2b-runtime/backup/s3.env
printf 'CONTROL_DATA=/data\nCONTROL_RUNTIMES=runtime1\n'
printf 'RUNTIME1_API_URL=http://127.0.0.1:3000\nRUNTIME1_ADMIN_URL=http://127.0.0.1:3012\nRUNTIME1_PROXY_URL=http://127.0.0.1:3002\n'
printf 'RUNTIME1_API_KEY=%s\n' "$E2B_API_KEY"
printf 'RUSTFS_ENDPOINT=%s\nRUSTFS_ACCESS_KEY=%s\nRUSTFS_SECRET_KEY=%s\nRUSTFS_BUCKET_NAME=%s\nRUSTFS_REGION=%s\n' \
  "$RUSTFS_ENDPOINT" "$RUSTFS_ACCESS_KEY" "$RUSTFS_SECRET_KEY" "$RUSTFS_BUCKET_NAME" "$RUSTFS_REGION"
ENV
printf 'CONTROL_LISTEN=%s\nCONTROL_ALLOWED_HOSTS=%s\nRUNTIME1_WEB_URL=%s\n' \
  "$CONTROL_LISTEN" "${CONTROL_ALLOWED_HOSTS:-}" "${RUNTIME1_WEB_URL:?}" >> "$stage/control.env"
sudo -n install -d -m 700 "$control"
sudo -n install -m 600 "$stage/control.env" "$control/control.env"
sudo -n install -m 755 "$stage/out/e2b-logic-control" "$control/e2b-logic-control"
sudo -n docker volume create e2b-control-single-data >/dev/null
volume=$(sudo -n docker volume inspect -f '{{.Mountpoint}}' e2b-control-single-data)
sudo -n chown 65532:65532 "$volume"
sudo -n chmod 700 "$volume"
if sudo -n docker container inspect e2b-logic-control >/dev/null 2>&1; then
  sudo -n docker rm -f e2b-logic-control >/dev/null
fi
sudo -n docker run -d --name e2b-logic-control --restart unless-stopped \
  --network host --read-only --cap-drop ALL --security-opt no-new-privileges \
  --env-file "$control/control.env" -v e2b-control-single-data:/data e2b-logic-control:single >/dev/null
for i in $(seq 1 30); do
  if curl -fsS --max-time 2 "$CONTROL_URL/health" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS --max-time 10 "$CONTROL_URL/health"
if [[ $action != control-deploy ]]; then
  verify
  sudo -n bash "$runtime/runtime.sh" smoke
fi
echo "Single-host $action completed."
