#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# shellcheck source=deploy/lib/install-dependencies.sh
source "$REPO_DIR/deploy/lib/install-dependencies.sh"

cascade_validate_supported_os ubuntu 22.04
cascade_validate_supported_os ubuntu 24.04
cascade_validate_supported_os debian 13
if cascade_validate_supported_os fedora 42 2>/dev/null; then
  echo "Unsupported operating system was accepted." >&2
  exit 1
fi

printf 'verified dependency\n' > "$TMP_DIR/payload"
PAYLOAD_SHA=$(sha256sum "$TMP_DIR/payload" | awk '{print $1}')
cascade_verify_sha256 "$TMP_DIR/payload" "$PAYLOAD_SHA"
if cascade_verify_sha256 "$TMP_DIR/payload" "$(printf '0%.0s' {1..64})" 2>/dev/null; then
  echo "Invalid SHA-256 was accepted." >&2
  exit 1
fi

FAKE_BIN="$TMP_DIR/bin"
mkdir -p "$FAKE_BIN"
cat > "$FAKE_BIN/gpg" <<EOF
#!/usr/bin/env bash
printf 'fpr:::::::::${CASCADE_DOCKER_GPG_FINGERPRINT}:\n'
EOF
chmod 0755 "$FAKE_BIN/gpg"
PATH="$FAKE_BIN:$PATH" cascade_verify_gpg_fingerprint "$TMP_DIR/payload" "$CASCADE_DOCKER_GPG_FINGERPRINT"
if PATH="$FAKE_BIN:$PATH" cascade_verify_gpg_fingerprint "$TMP_DIR/payload" "$(printf '0%.0s' {1..40})" 2>/dev/null; then
  echo "Invalid Docker key fingerprint was accepted." >&2
  exit 1
fi

cat > "$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$FAKE_BIN/dpkg" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod 0755 "$FAKE_BIN/docker" "$FAKE_BIN/dpkg"
if PATH="$FAKE_BIN:$PATH" cascade_docker_install_required; then
  echo "Existing Docker installation was not treated as reusable." >&2
  exit 1
fi

mkdir -p "$TMP_DIR/acme-home"
touch "$TMP_DIR/acme-home/acme.sh"
if cascade_acme_install_required "$TMP_DIR/acme-home/acme.sh"; then
  echo "Existing acme.sh installation was not treated as reusable." >&2
  exit 1
fi

echo "Dependency installer tests passed."
