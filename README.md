# sys-mon-mqtt

Publishes Linux host sensors to MQTT with Home Assistant discovery. A single static Go binary that replaces the Python [system_sensors](https://github.com/Sennevds/system_sensors) and keeps the same topics and entity IDs.

Sensors: temperature, clock_speed, disk_use, memory_use, cpu_usage, load_1m/5m/15m, swap_usage, last_boot, last_message, hostname, host_ip, host_os, host_arch, updates (pending apt upgrades), external_drives (disk usage or ZFS pool capacity).

## Install / update

Every push to `main` builds the binaries for amd64, arm64, armv7 and armv6 on the [`latest`](https://github.com/benmepham/sys-mon-mqtt/releases/tag/latest) release. The same command installs or updates:

```sh
curl -fsSL https://raw.githubusercontent.com/benmepham/sys-mon-mqtt/main/install.sh | sudo sh
```

This installs `/usr/local/bin/sys-mon-mqtt` and a systemd unit that runs as the user who ran sudo (override with `sudo SERVICE_USER=name sh`). It creates `/etc/sys-mon-mqtt/settings.yaml` from `settings_example.yaml` only if it doesn't already exist. The service needs no root.

## Settings

The Python `settings.yaml` format works as is. See `settings_example.yaml`. Unsupported keys (`timezone`, `display`, `fan_speed`, `net_tx`, …) are ignored. Every sensor is on unless set to `false`.

## Migrating from the Python version

1. Stop and disable the old service.
2. Copy its `settings.yaml` to `/etc/sys-mon-mqtt/settings.yaml` and keep the same `deviceName`.
3. Run the install command above.
4. Optional: remove entities for dropped sensors by clearing their retained configs, e.g.
   `mosquitto_pub -h <broker> -u <user> -P <pass> -r -n -t homeassistant/sensor/<devicename>/net_tx/config`
   (likewise `net_rx`, `fan_speed`, `wifi_strength`, `wifi_ssid`; `display` is under `homeassistant/switch/`, `power_status` under `homeassistant/binary_sensor/`).

## Build

Go is installed with [mise](https://mise.jdx.dev) (`mise.toml` tracks latest; `go.mod` sets the minimum).

```sh
mise install
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 mise exec -- go build -ldflags "-s -w" -o sys-mon-mqtt .
```

Other targets: `GOARCH=arm64`, or `GOARCH=arm GOARM=7` / `GOARM=6` for 32-bit Pis.

## Test with a local mosquitto

```sh
# With no config file, mosquitto listens on localhost and allows anonymous clients
podman run -d --init --name mqtt-test --network host docker.io/library/eclipse-mosquitto:2 mosquitto
mise exec -- go run . settings.yaml   # with mqtt.hostname: 127.0.0.1
podman exec mqtt-test mosquitto_sub -t 'homeassistant/#' -t 'system-sensors/#' -v
podman rm -f mqtt-test
```
