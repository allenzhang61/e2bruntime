#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
log=/var/log/e2b-runtime-provision.log
install -m 600 /dev/null "$log"
exec 3>&1
exec >>"$log" 2>&1
trap 'printf "Provision failed; inspect %s inside this VM.\n" "$log" >&3' ERR
printf 'Preparing Docker and nested KVM...\n' >&3
apt-get update
apt-get install -y ca-certificates curl iptables iproute2 rsync e2fsprogs lvm2
# Bento uses an LVM root with unused VG space. Expand only that known guest LV.
if [[ "$(findmnt -n -o SOURCE /)" == /dev/mapper/ubuntu--vg-ubuntu--lv ]]; then
  free_extents=$(vgs --noheadings -o vg_free_count ubuntu-vg | tr -d ' ')
  if (( free_extents > 0 )); then
    lvextend -r -l +100%FREE /dev/ubuntu-vg/ubuntu-lv
  fi
fi
modprobe kvm_amd 2>/dev/null || modprobe kvm_intel
modprobe tun
test -c /dev/kvm
test -c /dev/net/tun
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl --retry 3 -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${UBUNTU_CODENAME:-$VERSION_CODENAME}
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
systemctl enable --now docker
usermod -aG docker vagrant
docker compose version
install -d -m 700 /opt/e2b-runtime
install -m 600 /tmp/runtime-compose.yaml /opt/e2b-runtime/compose.yaml
install -m 600 /tmp/runtime-compose.env /opt/e2b-runtime/.env
install -m 755 /tmp/runtime.sh /opt/e2b-runtime/runtime.sh
rm -f /tmp/runtime-compose.yaml /tmp/runtime-compose.env /tmp/runtime.sh
printf 'Docker installed; starting independent Runtime stack.\n' >&3
/opt/e2b-runtime/runtime.sh up >&3
