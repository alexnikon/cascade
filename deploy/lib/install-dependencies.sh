#!/usr/bin/env bash
# Verified dependency installation helpers for Cascade setup.

CASCADE_DOCKER_GPG_FINGERPRINT="9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
CASCADE_ACME_SH_VERSION="3.1.4"
CASCADE_ACME_SH_SHA256="e5f8e187bbf5251e0cd8891f2622daab9850366bd17bea9f92c2fe2ee091fd32"

cascade_validate_supported_os() {
  local os_id="$1" os_version="$2"
  case "${os_id}:${os_version}" in
    ubuntu:22.04|ubuntu:24.04|debian:13) return 0 ;;
    *)
      echo "Unsupported operating system: ${os_id} ${os_version}. Supported: Ubuntu 22.04/24.04 and Debian 13." >&2
      return 1
      ;;
  esac
}

cascade_verify_sha256() {
  local file="$1" expected="$2" actual
  actual=$(sha256sum "$file" | awk '{print $1}')
  [[ "$actual" == "$expected" ]] || {
    echo "SHA-256 mismatch for $file: expected $expected, got $actual" >&2
    return 1
  }
}

cascade_verify_gpg_fingerprint() {
  local key_file="$1" expected="$2" actual gnupg_home
  gnupg_home=$(mktemp -d)
  chmod 0700 "$gnupg_home"
  actual=$(GNUPGHOME="$gnupg_home" gpg --batch --show-keys --with-colons "$key_file" 2>/dev/null \
    | awk -F: '$1 == "fpr" { print toupper($10); exit }' || true)
  rm -rf "$gnupg_home"
  expected=$(printf '%s' "$expected" | tr -d '[:space:]' | tr '[:lower:]' '[:upper:]')
  [[ -n "$actual" && "$actual" == "$expected" ]] || {
    echo "GPG fingerprint mismatch for $key_file: expected $expected, got ${actual:-none}" >&2
    return 1
  }
}

cascade_docker_install_required() {
  ! command -v docker >/dev/null 2>&1 && return 0
  dpkg -l docker.io 2>/dev/null | grep -q '^ii' && return 0
  return 1
}

cascade_acme_install_required() {
  [[ ! -f "${1:-$HOME/.acme.sh/acme.sh}" ]]
}

cascade_install_docker_ce() (
  local os_id="$1" os_codename="$2" os_version="$3" keyring_dir key_file arch tmp_dir
  cascade_validate_supported_os "$os_id" "$os_version"

  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl gnupg

  keyring_dir="/etc/apt/keyrings"
  key_file="$keyring_dir/docker.asc"
  install -m 0755 -d "$keyring_dir"
  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT
  curl -fL --retry 3 --retry-delay 2 \
    -o "$tmp_dir/docker.asc" "https://download.docker.com/linux/${os_id}/gpg"
  cascade_verify_gpg_fingerprint "$tmp_dir/docker.asc" "$CASCADE_DOCKER_GPG_FINGERPRINT"
  install -m 0644 "$tmp_dir/docker.asc" "$key_file"

  arch=$(dpkg --print-architecture)
  printf 'deb [arch=%s signed-by=%s] https://download.docker.com/linux/%s %s stable\n' \
    "$arch" "$key_file" "$os_id" "$os_codename" \
    > /etc/apt/sources.list.d/docker.list

  systemctl stop docker.socket docker.service 2>/dev/null || true
  DEBIAN_FRONTEND=noninteractive apt-get remove -y docker.io docker-compose docker-compose-v2 \
    docker-doc docker-buildx podman-docker containerd runc 2>/dev/null || true
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable docker --now
)

cascade_install_acme_sh() (
  local email="${1:-}" tmp_dir archive source_dir
  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT
  archive="$tmp_dir/acme-sh-${CASCADE_ACME_SH_VERSION}.tar.gz"
  source_dir="$tmp_dir/acme.sh-${CASCADE_ACME_SH_VERSION}"

  curl -fL --retry 3 --retry-delay 2 -o "$archive" \
    "https://github.com/acmesh-official/acme.sh/archive/refs/tags/${CASCADE_ACME_SH_VERSION}.tar.gz"
  cascade_verify_sha256 "$archive" "$CASCADE_ACME_SH_SHA256"
  tar -xzf "$archive" -C "$tmp_dir"

  local args=(--install --home "$HOME/.acme.sh" --no-profile --no-cron)
  [[ -n "$email" ]] && args+=(--email "$email")
  (cd "$source_dir" && ./acme.sh "${args[@]}")
)
