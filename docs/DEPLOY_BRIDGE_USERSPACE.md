# Bridge deployment with userspace AmneziaWG

This mode runs Cascade on a Docker bridge network, exposes an explicit UDP port
range, uses `/dev/net/tun`, and does not require Caddy. It is useful when host
networking is unavailable or when container port publication is preferred.

## Prerequisites

- Linux host with Docker Engine and Compose v2.
- `/dev/net/tun` available to the container.
- `NET_ADMIN` capability and the sysctls required for forwarding.
- A published TCP port for the web UI and a published UDP range matching the
  configured interface port pool.

Verify TUN support:

```sh
test -c /dev/net/tun
ls -l /dev/net/tun
```

Load the host module when needed:

```sh
sudo modprobe tun
```

## Compose example

Use an exact release tag in production:

```yaml
services:
  cascade:
    image: ghcr.io/alexnikon/cascade:X.Y.Z
    restart: unless-stopped
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    ports:
      - "8888:8888/tcp"
      - "51820-51849:51820-51849/udp"
    sysctls:
      net.ipv4.ip_forward: "1"
      net.ipv4.conf.all.src_valid_mark: "1"
    volumes:
      - ./data:/etc/wireguard
    environment:
      PORT: "8888"
```

Configure the Cascade port pool to match the published UDP range. A listen port
outside that range can work inside the container but cannot receive packets from
the host network.

Start and inspect the deployment:

```sh
docker compose up -d
docker compose ps
docker compose logs --tail=100 cascade
```

## Access

For an administrative UI without a reverse proxy, prefer an SSH tunnel:

```sh
ssh -L 8888:127.0.0.1:8888 user@example-host
```

Then open `http://127.0.0.1:8888`. Direct HTTP exposure should be limited to an
isolated test network.

Check the health endpoint from the host:

```sh
curl -fsS http://127.0.0.1:8888/health
```

## Tunnel verification

After starting an interface in the UI:

```sh
docker compose exec cascade test -c /dev/net/tun
docker compose exec cascade command -v amneziawg-go
docker compose exec cascade ip link show
docker compose exec cascade ss -lun
```

Allow the selected UDP range through the host firewall. For example with UFW:

```sh
sudo ufw allow 51820:51849/udp
```

Connect a client, verify that it can reach the tunnel gateway, and then verify
the expected public egress address. These checks distinguish handshake problems
from forwarding or NAT problems.

## Rootless Docker notes

Rootless deployments depend on host policy:

- The user running Docker must be able to open `/dev/net/tun`.
- Container sysctls unavailable in the user namespace must be configured on the
  host by an administrator.
- Published UDP ranges and firewall rules remain host responsibilities.
- Capability behavior varies by rootless runtime; verify interface creation and
  forwarding before relying on this mode.

## Updating

Change the exact release tag in the active Compose file, pull it, and run
`docker compose up -d`. Validate health, UI login, tunnel state, and the actual
container image before removing the previous local image.

See [deployment-modes.md](deployment-modes.md) and
[ARTIFACT_DEPLOYMENT.md](ARTIFACT_DEPLOYMENT.md) for the portable release flow.
