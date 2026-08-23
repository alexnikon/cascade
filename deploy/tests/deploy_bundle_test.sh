#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

VERSION="v1.2.3"
bash "$REPO_DIR/scripts/build-deploy-bundle.sh" "$VERSION" "$TMP_DIR"

ARCHIVE="$TMP_DIR/cascade-deploy-${VERSION}.tar.gz"
[[ -f "$ARCHIVE" ]]
[[ -f "$ARCHIVE.sha256" ]]
[[ -x "$TMP_DIR/install.sh" ]]
[[ -f "$TMP_DIR/install.sh.sha256" ]]
[[ -f "$TMP_DIR/SHA256SUMS" ]]
[[ ! -e "$TMP_DIR/release-manifest.json" ]]
[[ ! -e "$TMP_DIR/update-manifest.json" ]]
bash -n "$TMP_DIR/install.sh"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$TMP_DIR" && sha256sum -c "$(basename "$ARCHIVE").sha256")
else
  (cd "$TMP_DIR" && shasum -a 256 -c "$(basename "$ARCHIVE").sha256")
fi
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$TMP_DIR" && sha256sum -c SHA256SUMS)
else
  (cd "$TMP_DIR" && shasum -a 256 -c SHA256SUMS)
fi
grep -q "DEFAULT_VERSION=\"$VERSION\"" "$TMP_DIR/install.sh"
if bash "$REPO_DIR/scripts/build-deploy-bundle.sh" latest "$TMP_DIR/bad"; then
  echo "Bundle builder accepted a non-version release tag." >&2
  exit 1
fi
if grep -Eq 'git pull|build\.sh' "$REPO_DIR/deploy/setup.sh"; then
  echo "Setup still depends on a Git checkout or local image build." >&2
  exit 1
fi

CONTENTS=$(tar -tzf "$ARCHIVE")
grep -q '^cascade/docker-compose.yml$' <<< "$CONTENTS"
grep -q '^cascade/deploy/setup.sh$' <<< "$CONTENTS"
grep -q '^cascade/deploy/lib/install-dependencies.sh$' <<< "$CONTENTS"
grep -q '^cascade/deploy/caddy/Caddyfile$' <<< "$CONTENTS"
tar -xOf "$ARCHIVE" cascade/docker-compose.yml | grep -q 'image: ghcr.io/alexnikon/cascade:1.2.3'
tar -xOf "$ARCHIVE" cascade/deploy/docker-compose.bridge.yml.example | grep -q 'image: ghcr.io/alexnikon/cascade:1.2.3'
if tar -xOf "$ARCHIVE" cascade/docker-compose.yml | grep -q 'CASCADE_IMAGE'; then
  echo "Host Compose still uses CASCADE_IMAGE." >&2
  exit 1
fi
if grep -Eq '(^|/)(release-manifest\.json|update-manifest\.json|update\.sh|\.releases)(/|$)' <<< "$CONTENTS"; then
  echo "Bundle contains legacy updater state." >&2
  exit 1
fi
if grep -Eq '^cascade/(LICENSE|NOTICE|THIRD_PARTY_NOTICES\.md)$' <<< "$CONTENTS"; then
  echo "Bundle contains repository-only legal files." >&2
  exit 1
fi
if grep -Eq '(^|/)(\.git|go\.mod|internal|frontend|docs)(/|$)' <<< "$CONTENTS"; then
  echo "Bundle contains source or repository metadata." >&2
  exit 1
fi
if grep -q 'CASCADE_IMAGE' "$REPO_DIR/deploy/setup.sh" "$REPO_DIR/.env.example"; then
  echo "Runtime configuration still uses CASCADE_IMAGE." >&2
  exit 1
fi

echo "Deployment bundle test passed."
