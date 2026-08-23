#!/usr/bin/env bash
# Build the minimal server-side deployment bundle for a tagged release.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 VERSION OUTPUT_DIR" >&2
  exit 1
fi

VERSION="$1"
OUTPUT_DIR="$2"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE_TAG="${VERSION#v}"

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
  echo "Invalid version: $VERSION" >&2
  exit 1
}
STAGE_DIR=$(mktemp -d)
trap 'rm -rf "$STAGE_DIR"' EXIT
BUNDLE_DIR="$STAGE_DIR/cascade"
mkdir -p "$BUNDLE_DIR/deploy/caddy" "$BUNDLE_DIR/deploy/lib" "$OUTPUT_DIR"

sed "s|ghcr.io/alexnikon/cascade:[0-9][0-9A-Za-z.-]*|ghcr.io/alexnikon/cascade:${IMAGE_TAG}|" \
  "$REPO_DIR/docker-compose.yml" > "$BUNDLE_DIR/docker-compose.yml"
chmod 0644 "$BUNDLE_DIR/docker-compose.yml"
sed "s|ghcr.io/alexnikon/cascade:[0-9][0-9A-Za-z.-]*|ghcr.io/alexnikon/cascade:${IMAGE_TAG}|" \
  "$REPO_DIR/deploy/docker-compose.bridge.yml.example" > "$BUNDLE_DIR/deploy/docker-compose.bridge.yml.example"
chmod 0644 "$BUNDLE_DIR/deploy/docker-compose.bridge.yml.example"
install -m 0755 "$REPO_DIR/deploy/setup.sh" "$BUNDLE_DIR/deploy/setup.sh"
install -m 0755 "$REPO_DIR/deploy/switch-mode.sh" "$BUNDLE_DIR/deploy/switch-mode.sh"
install -m 0755 "$REPO_DIR/deploy/tcp_tune.sh" "$BUNDLE_DIR/deploy/tcp_tune.sh"
install -m 0644 "$REPO_DIR/deploy/lib/install-dependencies.sh" "$BUNDLE_DIR/deploy/lib/install-dependencies.sh"
install -m 0644 "$REPO_DIR/deploy/caddy/docker-compose.yml" "$BUNDLE_DIR/deploy/caddy/docker-compose.yml"
install -m 0644 "$REPO_DIR/deploy/caddy/Caddyfile" "$BUNDLE_DIR/deploy/caddy/Caddyfile"
cp -R "$REPO_DIR/deploy/caddy/www" "$BUNDLE_DIR/deploy/caddy/www"

ASSET="cascade-deploy-${VERSION}.tar.gz"
tar -czf "$OUTPUT_DIR/$ASSET" -C "$STAGE_DIR" cascade
(
  cd "$OUTPUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$ASSET" > "$ASSET.sha256"
  else
    shasum -a 256 "$ASSET" > "$ASSET.sha256"
  fi
)
sed "s/__CASCADE_RELEASE__/$VERSION/g" "$REPO_DIR/deploy/install.sh" > "$OUTPUT_DIR/install.sh"
chmod 0755 "$OUTPUT_DIR/install.sh"
(
  cd "$OUTPUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$ASSET" install.sh > SHA256SUMS
    sha256sum install.sh > install.sh.sha256
  else
    shasum -a 256 "$ASSET" install.sh > SHA256SUMS
    shasum -a 256 install.sh > install.sh.sha256
  fi
)

echo "Built $OUTPUT_DIR/$ASSET"
