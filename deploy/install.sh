#!/usr/bin/env bash
# Install a versioned Cascade deployment bundle without cloning the repository.
set -euo pipefail

DEFAULT_VERSION="__CASCADE_RELEASE__"
REPOSITORY="${CASCADE_REPOSITORY:-alexnikon/cascade}"
INSTALL_DIR="${CASCADE_INSTALL_DIR:-/opt/cascade}"
VERSION="$DEFAULT_VERSION"
SETUP_ARGS=()

usage() {
  cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--install-dir PATH] [setup options]

Options:
  --version VERSION    Install a specific tagged release
  --install-dir PATH   Runtime directory (default: /opt/cascade)
  --yes                Use setup defaults
  --staging            Use the Let's Encrypt staging CA
  --prefer-ipv6        Prefer IPv6 when detecting the public address
  -h, --help           Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || { echo "Missing value for --version" >&2; exit 1; }
      VERSION="$2"
      shift 2
      ;;
    --install-dir)
      [[ $# -ge 2 ]] || { echo "Missing value for --install-dir" >&2; exit 1; }
      INSTALL_DIR="$2"
      shift 2
      ;;
    --yes|--staging|--prefer-ipv6)
      SETUP_ARGS+=("$1")
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

[[ "$(id -u)" -eq 0 ]] || { echo "Run as root." >&2; exit 1; }
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
  echo "Invalid release version: $VERSION" >&2
  exit 1
}
[[ "$INSTALL_DIR" == /* && "$INSTALL_DIR" != "/" ]] || {
  echo "Install directory must be an absolute path other than /." >&2
  exit 1
}

if [[ -d "$INSTALL_DIR" && -n "$(find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
  echo "Install directory is not empty: $INSTALL_DIR" >&2
  exit 1
fi

ASSET="cascade-deploy-${VERSION}.tar.gz"
BASE_URL="${CASCADE_RELEASE_BASE_URL:-https://github.com/${REPOSITORY}/releases/download/${VERSION}}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading Cascade $VERSION deployment bundle..."
curl -fL --retry 3 --retry-delay 2 -o "$TMP_DIR/$ASSET" "$BASE_URL/$ASSET"
curl -fL --retry 3 --retry-delay 2 -o "$TMP_DIR/$ASSET.sha256" "$BASE_URL/$ASSET.sha256"

(
  cd "$TMP_DIR"
  sha256sum -c "$ASSET.sha256"
)

if tar -tzf "$TMP_DIR/$ASSET" | grep -Ev '^cascade(/|$)' | grep -q .; then
  echo "Deployment archive contains an unexpected path." >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
tar -xzf "$TMP_DIR/$ASSET" --strip-components=1 -C "$INSTALL_DIR"
chmod 0755 "$INSTALL_DIR/deploy/setup.sh" "$INSTALL_DIR/deploy/switch-mode.sh" \
  "$INSTALL_DIR/deploy/tcp_tune.sh"

echo "Runtime files installed in $INSTALL_DIR"
bash "$INSTALL_DIR/deploy/setup.sh" "${SETUP_ARGS[@]}"
