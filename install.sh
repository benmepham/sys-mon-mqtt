#!/bin/sh
# Install or update sys-mon-mqtt from the latest build. Run with sudo.
# The service runs as SERVICE_USER (defaults to the user who ran sudo).
set -eu

REPO=benmepham/sys-mon-mqtt
RAW=https://raw.githubusercontent.com/$REPO/main
SERVICE_USER=${SERVICE_USER:-${SUDO_USER:-}}

if [ "$(id -u)" -ne 0 ]; then
	echo "Run with sudo" >&2
	exit 1
fi
if [ -z "$SERVICE_USER" ] || [ "$SERVICE_USER" = root ]; then
	echo "Set SERVICE_USER to a non-root user" >&2
	exit 1
fi

case "$(uname -m)" in
x86_64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
armv7l) arch=armv7 ;;
armv6l) arch=armv6 ;;
*)
	echo "Unsupported architecture: $(uname -m)" >&2
	exit 1
	;;
esac

tmp=$(mktemp)
curl -fsSL -o "$tmp" "https://github.com/$REPO/releases/download/latest/sys-mon-mqtt-linux-$arch"
install -m 755 "$tmp" /usr/local/bin/sys-mon-mqtt
rm -f "$tmp"

new_settings=false
if [ ! -f /etc/sys-mon-mqtt/settings.yaml ]; then
	install -d /etc/sys-mon-mqtt
	curl -fsSL -o /etc/sys-mon-mqtt/settings.yaml "$RAW/settings_example.yaml"
	new_settings=true
fi
# Settings hold the MQTT password
chown "$SERVICE_USER" /etc/sys-mon-mqtt/settings.yaml
chmod 600 /etc/sys-mon-mqtt/settings.yaml

curl -fsSL "$RAW/sys-mon-mqtt.service" | sed "s/^User=.*/User=$SERVICE_USER/" >/etc/systemd/system/sys-mon-mqtt.service
systemctl daemon-reload
systemctl enable sys-mon-mqtt

if [ "$new_settings" = true ]; then
	echo "Edit /etc/sys-mon-mqtt/settings.yaml, then: sudo systemctl start sys-mon-mqtt"
else
	systemctl restart sys-mon-mqtt
	echo "Updated and restarted sys-mon-mqtt"
fi
