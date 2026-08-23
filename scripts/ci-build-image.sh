#!/usr/bin/env bash
# Build and optionally push one or more OCI image tags without CI-provider state.
set -euo pipefail

if [[ $# -lt 4 ]]; then
  echo "Usage: $0 VERSION COMMIT MODE IMAGE [IMAGE ...]" >&2
  echo "MODE must be --load or --push." >&2
  exit 1
fi

VERSION="$1"
COMMIT="$2"
MODE="$3"
shift 3

[[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || { echo "Commit must be a full SHA." >&2; exit 1; }
[[ "$MODE" == "--load" || "$MODE" == "--push" ]] || { echo "Invalid mode: $MODE" >&2; exit 1; }

TAG_ARGS=()
for image in "$@"; do
  [[ "$image" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$ ]] || {
    echo "Invalid OCI image tag: $image" >&2
    exit 1
  }
  TAG_ARGS+=(--tag "$image")
done

docker buildx build \
  --platform linux/amd64 \
  --build-arg "VERSION=$VERSION" \
  --build-arg "GIT_COMMIT=$COMMIT" \
  "${TAG_ARGS[@]}" \
  "$MODE" \
  .
