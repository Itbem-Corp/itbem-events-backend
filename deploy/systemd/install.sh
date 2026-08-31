#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must run as root" >&2
  exit 1
fi
if [ "$#" -ne 1 ] || [ ! -x "$1" ]; then
  echo "usage: install.sh /path/to/reviewed/itbem-ai-agent" >&2
  exit 1
fi

source_binary=$1
asset_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
revision=$(sha256sum "$source_binary" | awk '{print $1}')
release_dir=/opt/itbem-ai-agent/releases/$revision

install -d -m 0755 "$release_dir" /opt/itbem-ai-agent /etc/itbem-ai-agent/roles /etc/itbem-ai-agent/disabled
install -d -m 0711 -o root -g root /etc/itbem-ai-agent/secrets
install -m 0755 "$source_binary" "$release_dir/itbem-ai-agent"
ln -sfn "$release_dir" /opt/itbem-ai-agent/current
install -m 0644 "$asset_dir/itbem-ai-agent@.service" /etc/systemd/system/itbem-ai-agent@.service
install -m 0644 "$asset_dir/itbem-ai-agent-doctor@.service" /etc/systemd/system/itbem-ai-agent-doctor@.service
install -d -m 0711 -o root -g root /srv/itbem-agent-workspaces

for lane in orchestration engineering review qa release; do
  account=itbem-agent-$lane
  if ! id "$account" >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "/var/lib/itbem-ai-agent/$lane" --create-home --shell /usr/sbin/nologin "$account"
  fi
  install -d -m 0700 -o "$account" -g "$account" "/var/lib/itbem-ai-agent/$lane"
  install -d -m 0700 -o "$account" -g "$account" "/srv/itbem-agent-workspaces/$lane"
  install -d -m 0750 -o root -g "$account" "/etc/itbem-ai-agent/secrets/$lane"
  if [ ! -e "/etc/itbem-ai-agent/roles/$lane.env" ]; then
    install -m 0600 -o root -g root "$asset_dir/roles/$lane.env.example" "/etc/itbem-ai-agent/roles/$lane.env"
  fi
done

if [ ! -e /etc/itbem-ai-agent/common.env ]; then
  install -m 0600 -o root -g root "$asset_dir/common.env.example" /etc/itbem-ai-agent/common.env
fi

for lane in orchestration review; do
  dropin=/etc/systemd/system/itbem-ai-agent@$lane.service.d
  install -d -m 0755 "$dropin"
  install -m 0644 "$asset_dir/readonly-workspaces.conf" "$dropin/readonly-workspaces.conf"
done

systemctl daemon-reload

echo "Installed reviewed binary revision $revision."
echo "Fill root-only common/role environment files, scoped AWS credentials and the separate review/release GitHub App PEMs."
echo "Place a separate managed checkout registry under each private /srv/itbem-agent-workspaces/<lane> root."
echo "Run each --doctor through systemctl before enabling any lane; this installer starts no service."
