# Installer validation — 2026-11-01

## Environment and boundaries

Tests ran on the disposable Debian 13 x86_64 VM at `10.0.0.9`, with kernel
`6.12.113+deb13-amd64`, approximately 4 GB RAM and 13 GB initially available disk.
The initial system had no Docker, curl or CA certificate package. Only curl and
CA certificates were installed before exercising the published installer;
setup installed Docker CE, Compose, ffmpeg and acme.sh itself.

The published baseline was release `v0.10.1`. Modified deployment bundles were
built locally with the same version to reuse its existing image, then supplied
through `CASCADE_RELEASE_BASE_URL=file:///var/tmp`. These were test bundles,
not newly published releases. Later scenarios reused installed system packages
and cached images; they were clean runtime installations, not fresh OS resets.

Kernel mode, public certificate issuance, trusted HTTPS, successful IPv6-only
installation and VPN client traffic were outside this run. A local self-signed
certificate labeled as staging let Caddy start without requesting a certificate.
The script's built-in HTTPS checks are not evidence of publicly valid TLS.

## Findings and fixes

1. **Interactive pipe installation exits silently.** The published command
   without `--yes` exited with status 1 at the first run-mode prompt. Setup read
   from the script pipe, reached EOF and exited under `set -e`. Both entrypoints
   now use `/dev/tty` for interactive input and reject missing terminals before
   deployment changes. This reproduces an installation failure; the original
   third-party report had no log, so an exact match cannot be established.
2. **Minimal-image prerequisites were undocumented.** The VM could not execute
   the README download command initially. README now includes curl/CA setup,
   and the installer checks required download/extraction commands explicitly.
3. **`--staging` lost to saved configuration.** Setup loaded `.env` after parsing
   the flag. Explicit staging now takes precedence; tested by setting saved
   `ACME_STAGING=0`, rerunning with `--staging` and verifying the saved value was 1.
4. **Bridge PortPool targeted the old database.** The old `wireguard.db` path
   produced `no such table: settings` against the actual installation. Setup now
   writes to `cascade.db`. The integration test verifies the saved pool, Docker
   UDP bindings and an automatically assigned port on a running userspace tunnel.
5. **Fresh instances fail the image's tunnel-based health command.** The image's
   `wg show | grep interface` command returned 1 while HTTP health returned `ok`.
   Host and bridge Compose configurations now check the API using the container's
   `PORT`, allowing a healthy instance with zero tunnel interfaces. The image
   itself was not rebuilt or changed.
6. **The swap step caused sysctl setup to skip its managed keys.** Step 0 created
   `99-cascade.conf` with only swappiness; Step 4 then skipped it because it
   existed. Setup now refreshes its own keys and preserves unrelated settings.
   TCP tuning already applied overlapping settings, so this finding alone does
   not establish a forwarding failure in the previous installation.

Setup also rejects unknown arguments before logging/deployment, supports help,
and prints update commands that quote custom paths and preserve bridge mode.
The installer handles an empty forwarded argument array on older Bash versions.

## Results

| Scenario | Result |
| --- | --- |
| Published default installer, userspace/host, `--yes --staging` | Exit 0; HTTP health OK |
| Published interactive installer through a pipe | Reproduced exit 1 at the first prompt |
| Modified installer, explicit `--version v0.10.1` | Installed the expected image |
| `--install-dir '/opt/cascade test'` | Exit 0; runtime paths and reruns worked |
| `--prefer-ipv6` without a default IPv6 route | Detected an IPv4 address successfully |
| Noninteractive rerun with saved configuration | Exit 0; configuration preserved |
| Interactive setup from a file | Exit 0; terminal answers accepted |
| Data preservation across setup | Control setting and active AWG interface survived; SQLite check OK |
| Interactive installer through a pipe, bridge, port 8891 | Exit 0; answers accepted through the terminal |
| Bridge pool `51840-51843` | API healthy; bindings and DB pool matched; userspace tunnel started inside the pool |
| Unattended bridge with HTTP healthcheck | Healthy with zero interfaces; smoke test created and removed a tunnel |
| Default host runtime with HTTP healthcheck | Healthy with zero interfaces; HTTP health and both SQLite checks OK |
| Invalid arguments, missing values, invalid version/path, occupied directory | Rejected before setup |
| Missing terminal, non-root execution, invalid checksum | Rejected; no target installation created for checksum/terminal failures |
| Missing GitHub release | curl exit 22 / HTTP 404; target absent |
| Final runtime contents | No repository checkout, source tree, release archives, checksums or updater manifests |

Twelve negative scenarios ran on Debian. Local tests additionally cover
`CASCADE_INSTALL_DIR`, missing curl, simulated download failure, argument boundaries
and the interactive pipe path using a pseudo-terminal.

## Upstream comparison

The supplied upstream checkout was commit
`02fc3fcb928c4518430e7aaa5dd5b08d419670f1` in
`/Users/alex/Developer/Cascade-upstream/cascade`.
Its quickstart scripts install Git, clone or pull the repository, export an AWG
run mode and host networking, then invoke `setup.sh --yes`. Its setup also pulls
the moving application image with a local-build fallback.

The useful pattern is explicit, documented installation presets. README now
distinguishes unattended and interactive commands and explains how to resume an
interrupted install. No upstream clone/build logic was imported. Cascade retains
versioned minimal bundles, checksum verification, pinned application images and
temporary extraction cleanup. The upstream database path is stale as well, so
copying its setup wholesale would retain that problem.

## Repeatable checks

Development checks require Bash and Python 3:

```bash
bash deploy/tests/deploy_bundle_test.sh
bash deploy/tests/install_artifact_test.sh
bash deploy/tests/install_dependencies_test.sh
bash deploy/tests/setup_preflight_test.sh
git diff --check
```

On an installed disposable userspace/bridge instance, with no users or with API
access otherwise available, run:

```bash
bash deploy/tests/bridge_install_test.sh 8891 51840-51843
```

The bridge test creates a temporary interface and deletes it on exit. It checks
HTTP health, Docker health, SQLite integrity, PortPool, UDP bindings, the actual
assigned port, the active link and the `amneziawg-go` process.

At the end of validation, the VM retained `/opt/cascade` in userspace/host mode with image
`ghcr.io/alexnikon/cascade:0.10.1`, no users or test tunnels, and a test certificate.
Its image digest is
`sha256:a68149a0a9010e6868ef1c1369183a0fbde4c4bf45fe7d62befbf1579d0608e1`.
Raw evidence is saved locally in `/tmp/cascade-install-audit-results`;
temporary test runtimes and transferred bundles are removed from the VM.
No commit, push, release or production changes were made.

After validation, the runtime, test certificates, renewal job and managed host
configuration were removed at the user's request for manual reinstallation.
Docker and installed system packages were retained. The fixes were then prepared
for commit and push; publishing a new release remains a separate step.
