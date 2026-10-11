#!/usr/bin/env bash
set -euo pipefail
umask 077
work=$1
cd /home/zyl/e2bruntime/deploy/two-vm
sudo -n install -d -m 700 /opt/e2b-control
cfg=$(mktemp)
secret=$(mktemp)
trap 'rm -f "$cfg" "$secret"' EXIT
for node in runtime1 runtime2; do
 vagrant ssh-config "$node" > "$cfg"
 chmod 600 "$cfg"
 install -m 600 "$cfg" "$work/$node.ssh"
 # Fetch credentials directly to the 211 host, without printing or sending them to Mac.
 key=$(ssh -F "$cfg" "$node" 'sudo bash -s' <<'KEY'
set -a
source /opt/e2b-runtime/sdk.env
printf '%s' "$E2B_API_KEY"
KEY
 )
 [[ -n "$key" ]] || { echo "Missing $node key"; exit 1; }
 upper=RUNTIME1; first=14010; admin=14012; web=14001; proxy=14013
 [[ "$node" != runtime2 ]] || { upper=RUNTIME2; first=14020; admin=14022; web=14011; proxy=14023; }
 printf '%s_API_KEY=%s\n' "$upper" "$key" >> "$secret"
 unset key
 cat > "$work/$node-tunnel.service" <<UNIT
[Unit]
Description=Logic control private $node API tunnel
After=network-online.target
Wants=network-online.target
[Service]
User=zyl
ExecStart=/usr/bin/ssh -N -F $work/$node.ssh -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -L 127.0.0.1:$first:127.0.0.1:3000 -L 127.0.0.1:$admin:127.0.0.1:3012 -L 127.0.0.1:$web:127.0.0.1:3001 -L 127.0.0.1:$proxy:127.0.0.1:3002 $node
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
UNIT
 sudo -n install -m 644 "$work/$node-tunnel.service" "/etc/systemd/system/e2b-control-$node.service"
done
# The RustFS env uses shell-safe simple values in this development deployment.
cat /home/zyl/e2bruntime/dev/deploy/rustfs/.env >> "$secret"
printf '\nCONTROL_LISTEN=127.0.0.1:13021\nCONTROL_DATA=/data\n' >> "$secret"
sudo -n install -m 600 "$secret" /opt/e2b-control/control.env
sudo -n systemctl daemon-reload
sudo -n systemctl enable --now e2b-control-runtime1.service e2b-control-runtime2.service
sudo -n systemctl restart e2b-control-runtime1.service e2b-control-runtime2.service
sudo -n docker build -t e2b-logic-control:dev "$work"
sudo -n docker volume create e2b-control-data >/dev/null
volume=$(sudo -n docker volume inspect -f '{{.Mountpoint}}' e2b-control-data)
sudo -n chown 65532:65532 "$volume"
sudo -n chmod 700 "$volume"
sudo -n docker rm -f e2b-logic-control >/dev/null 2>&1 || true
sudo -n docker run -d --name e2b-logic-control --restart unless-stopped --network host --read-only --cap-drop ALL --security-opt no-new-privileges --env-file /opt/e2b-control/control.env -v e2b-control-data:/data e2b-logic-control:dev >/dev/null
for i in $(seq 1 30); do if curl -fsS http://127.0.0.1:13021/health >/dev/null; then echo 'Logic control healthy on 127.0.0.1:13021'; exit 0; fi; sleep 1; done
exit 1
