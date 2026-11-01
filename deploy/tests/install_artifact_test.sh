#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
DIST_DIR="$TMP_DIR/dist"
RUNTIME_DIR="$TMP_DIR/runtime"
FAKE_BIN="$TMP_DIR/bin"
bash "$REPO_DIR/scripts/build-deploy-bundle.sh" v2.0.0 "$DIST_DIR"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/id" <<'EOF'
#!/bin/bash
[[ "${1:-}" == "-u" ]] && echo "${CASCADE_TEST_UID:-0}"
EOF
cat > "$FAKE_BIN/curl" <<'EOF'
#!/bin/bash
[[ "${CASCADE_TEST_DOWNLOAD_FAIL:-0}" == 0 ]] || exit 22
output=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      output="$2"
      shift 2
      ;;
    http*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
cp "$CASCADE_TEST_DIST/${url##*/}" "$output"
EOF
cat > "$FAKE_BIN/sha256sum" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-c" ]]; then
  exec /usr/bin/shasum -a 256 -c "$2"
fi
exec /usr/bin/shasum -a 256 "$@"
EOF
cat > "$FAKE_BIN/bash" <<'EOF'
#!/bin/bash
printf '%s\n' "$@" > "$CASCADE_TEST_SETUP_CALL"
if [[ "${CASCADE_TEST_PROMPT:-0}" == 1 ]]; then
  printf 'Setup choice: '
  read -r choice
  printf '%s\n' "$choice" >> "$CASCADE_TEST_SETUP_CALL"
fi
EOF
chmod 0755 "$FAKE_BIN/id" "$FAKE_BIN/curl" "$FAKE_BIN/sha256sum" "$FAKE_BIN/bash"

CASCADE_TEST_DIST="$DIST_DIR" CASCADE_TEST_SETUP_CALL="$TMP_DIR/setup-call" \
  PATH="$FAKE_BIN:/usr/bin:/bin" /bin/bash "$DIST_DIR/install.sh" \
  --version v2.0.0 --install-dir "$RUNTIME_DIR" --yes

printf '%s\n' "$RUNTIME_DIR/deploy/setup.sh" --yes > "$TMP_DIR/expected-call"
cmp "$TMP_DIR/expected-call" "$TMP_DIR/setup-call"
grep -q 'image: ghcr.io/alexnikon/cascade:2.0.0' "$RUNTIME_DIR/docker-compose.yml"
[[ ! -e "$RUNTIME_DIR/release-manifest.json" ]]
[[ ! -e "$RUNTIME_DIR/.releases" ]]
[[ ! -e "$RUNTIME_DIR/deploy/update.sh" ]]
[[ ! -e "$RUNTIME_DIR/.git" ]]

printf 'ADMIN_PATH=test\n' > "$RUNTIME_DIR/.env"
mkdir -p "$RUNTIME_DIR/data"
printf 'database-placeholder\n' > "$RUNTIME_DIR/data/cascade.db"
sed -i.bak 's|cascade:2\.0\.0|cascade:2.0.1|' "$RUNTIME_DIR/docker-compose.yml"
grep -q 'image: ghcr.io/alexnikon/cascade:2.0.1' "$RUNTIME_DIR/docker-compose.yml"
grep -q '^ADMIN_PATH=test$' "$RUNTIME_DIR/.env"
grep -q '^database-placeholder$' "$RUNTIME_DIR/data/cascade.db"
rm "$RUNTIME_DIR/docker-compose.yml.bak"

BAD_DIST_DIR="$TMP_DIR/bad-dist"
BAD_RUNTIME_DIR="$TMP_DIR/bad-runtime"
cp -R "$DIST_DIR" "$BAD_DIST_DIR"
printf '0%.0s' {1..64} > "$BAD_DIST_DIR/cascade-deploy-v2.0.0.tar.gz.sha256"
printf '  cascade-deploy-v2.0.0.tar.gz\n' >> "$BAD_DIST_DIR/cascade-deploy-v2.0.0.tar.gz.sha256"
if CASCADE_TEST_DIST="$BAD_DIST_DIR" CASCADE_TEST_SETUP_CALL="$TMP_DIR/bad-setup-call" \
  PATH="$FAKE_BIN:/usr/bin:/bin" /bin/bash "$BAD_DIST_DIR/install.sh" \
  --version v2.0.0 --install-dir "$BAD_RUNTIME_DIR" --yes; then
  echo "Installer accepted an invalid deployment checksum." >&2
  exit 1
fi
[[ ! -e "$BAD_RUNTIME_DIR/docker-compose.yml" ]]

# Exercise argument boundaries, environment defaults and rejection before setup.
export CASCADE_TEST_DIST="$DIST_DIR" CASCADE_TEST_SETUP_CALL="$TMP_DIR/matrix-call"
export PATH="$FAKE_BIN:/usr/bin:/bin"
CASCADE_INSTALL_DIR="$TMP_DIR/runtime with spaces" /bin/bash "$DIST_DIR/install.sh" \
  --yes --staging --prefer-ipv6
printf '%s\n' "$TMP_DIR/runtime with spaces/deploy/setup.sh" \
  --yes --staging --prefer-ipv6 > "$TMP_DIR/expected-call"
cmp "$TMP_DIR/expected-call" "$CASCADE_TEST_SETUP_CALL"

reject() {
  local expected="$1"
  shift
  /bin/rm -f "$CASCADE_TEST_SETUP_CALL"
  if /bin/bash "$DIST_DIR/install.sh" "$@" > "$TMP_DIR/rejection.log" 2>&1; then
    echo "Installer unexpectedly accepted: $*" >&2
    exit 1
  fi
  /usr/bin/grep -q -- "$expected" "$TMP_DIR/rejection.log"
  [[ ! -e "$CASCADE_TEST_SETUP_CALL" ]]
}
reject 'Unknown argument' --unknown
reject 'Missing value' --version
reject 'Missing value' --install-dir
reject 'Invalid release version' --version latest --yes
reject 'absolute path' --install-dir relative --yes
reject 'absolute path' --install-dir / --yes
reject 'not empty' --install-dir "$RUNTIME_DIR" --yes
CASCADE_TEST_UID=1000 reject 'Run as root' --yes
CASCADE_TEST_DOWNLOAD_FAIL=1 reject 'Downloading Cascade' \
  --install-dir "$TMP_DIR/download-failed" --yes
[[ ! -e "$TMP_DIR/download-failed" ]]
mkdir -p "$TMP_DIR/missing-bin"
cp "$FAKE_BIN/id" "$TMP_DIR/missing-bin/id"
PATH="$TMP_DIR/missing-bin" reject 'Missing required command: curl' --yes
grep -q '^ADMIN_PATH=test$' "$RUNTIME_DIR/.env"
grep -q '^database-placeholder$' "$RUNTIME_DIR/data/cascade.db"

# A separate session has no controlling terminal even when the test runner does.
# A PTY reproduces the documented pipe command with answers on /dev/tty.
export CASCADE_TEST_INSTALLER="$DIST_DIR/install.sh" CASCADE_TEST_TMP="$TMP_DIR"
python3 - <<'PY'
import errno
import os
import pty
import select
import subprocess
import time

installer = os.environ['CASCADE_TEST_INSTALLER']
runtime = os.environ['CASCADE_TEST_TMP'] + '/no-terminal'
result = subprocess.run(['/bin/bash', installer, '--install-dir', runtime],
                        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                        stderr=subprocess.STDOUT, start_new_session=True)
assert result.returncode != 0, result.stdout
assert b'Interactive setup requires a terminal' in result.stdout, result.stdout
assert not os.path.exists(runtime)

os.environ['CASCADE_TEST_PROMPT'] = '1'
runtime = os.environ['CASCADE_TEST_TMP'] + '/interactive'
pid, fd = pty.fork()
if pid == 0:
    os.execl('/bin/bash', '/bin/bash', '-c',
             'cat "$1" | /bin/bash -s -- --install-dir "$2"',
             'pipe-test', installer, runtime)
output = b''
answered = False
deadline = time.monotonic() + 20
try:
    while time.monotonic() < deadline:
        if not select.select([fd], [], [], 0.2)[0]:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError as exc:
            if exc.errno == errno.EIO:
                break
            raise
        if not chunk:
            break
        output += chunk
        if b'Setup choice:' in output and not answered:
            os.write(fd, b'userspace\n')
            answered = True
    else:
        os.kill(pid, 9)
        raise AssertionError('Interactive installer timed out: ' + repr(output))
finally:
    os.close(fd)
_, status = os.waitpid(pid, 0)
assert status == 0, output
assert answered, output
with open(os.environ['CASCADE_TEST_SETUP_CALL']) as call:
    assert call.read().splitlines() == [runtime + '/deploy/setup.sh', 'userspace']
PY

echo "Artifact installer test passed."
