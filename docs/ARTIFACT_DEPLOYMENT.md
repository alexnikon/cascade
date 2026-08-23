# Artifact-only deployment

Tagged releases publish a minimal deployment bundle alongside the versioned Cascade
image. The server receives Compose files, runtime scripts, Caddy configuration, and
static decoy files. It does not receive the Git repository or application source.

## Publish a release

Deployment assets are created only for tags matching `v*`. The bundle builder writes
the corresponding exact image tag, without the leading `v`, into both Compose modes.

```bash
git tag -a vX.Y.Z -m "Cascade vX.Y.Z"
git push origin vX.Y.Z
```

The release contains:

- `cascade-deploy-vX.Y.Z.tar.gz`
- `cascade-deploy-vX.Y.Z.tar.gz.sha256`
- `install.sh`
- `install.sh.sha256`
- `SHA256SUMS`

## Install a new server

```bash
VERSION=v0.9.7
CASCADE_INSTALLER_DIR=$(mktemp -d)
CASCADE_RELEASE_URL="https://github.com/alexnikon/cascade/releases/download/${VERSION}"
curl -fL --retry 3 -o "$CASCADE_INSTALLER_DIR/install.sh" "$CASCADE_RELEASE_URL/install.sh"
curl -fL --retry 3 -o "$CASCADE_INSTALLER_DIR/install.sh.sha256" "$CASCADE_RELEASE_URL/install.sh.sha256"
(cd "$CASCADE_INSTALLER_DIR" && sha256sum -c install.sh.sha256)
sudo bash "$CASCADE_INSTALLER_DIR/install.sh" --version "$VERSION" --yes
```

Omit `--yes` for interactive setup. Runtime data and configuration are stored in
`/opt/cascade/data` and `/opt/cascade/.env`. TLS and ACME state remain outside the
runtime directory.

## Verify the installation

```bash
test ! -d /opt/cascade/.git
test ! -e /opt/cascade/release-manifest.json
test ! -e /opt/cascade/.releases
grep 'image: ghcr.io/alexnikon/cascade:' /opt/cascade/docker-compose.yml
sudo docker inspect cascade --format '{{.Config.Image}}'
curl -fsS http://127.0.0.1:8888/api/health
```

## Update or roll back

Edit the exact image tag in `/opt/cascade/docker-compose.yml`, then apply it:

```bash
cd /opt/cascade
sudo docker compose pull
sudo docker compose up -d
curl -fsS http://127.0.0.1:8888/api/health
```

For rollback, restore the previous tag and repeat the same commands. Compose preserves
`.env`, `data/`, certificates, and Caddy state because they are outside the container.
