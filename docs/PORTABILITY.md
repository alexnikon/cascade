# Portability and External Dependencies

Cascade is maintained at `github.com/alexnikon/cascade`. The source module,
runtime, deployment bundle, and OCI image format do not require
access to the upstream Cascade repository. A local `upstream` Git remote may be
kept as an optional, read-only source for manually selected fixes.

## Release portability

The official GitHub workflow is an adapter around repository scripts:

```sh
bash scripts/ci-test.sh
bash scripts/check-autonomy.sh
bash scripts/ci-build-image.sh v1.2.3 FULL_COMMIT_SHA --push registry.example/cascade:1.2.3
bash scripts/build-deploy-bundle.sh v1.2.3 dist
```

`CASCADE_RELEASE_BASE_URL` selects an arbitrary HTTPS artifact directory for
installation. `CASCADE_REPOSITORY` remains a GitHub-compatible shorthand when no
base URL is supplied. The deployment bundle contains the exact version tag in its
Compose files. Operators update or roll back by editing that tag and applying Compose.

The application update checker reads the latest release from the official GitHub API.
It is informational and is not part of the deployment mechanism.

## External dependency inventory

- The build uses pinned Go modules from `go.mod` and `go.sum`.
- The runtime image derives from a version-and-digest-pinned AmneziaWG image.
- Alpine packages and Docker CE packages are downloaded from their distribution
  repositories during image or host setup.
- Kernel mode uses the Amnezia PPA and is therefore operationally dependent on
  that repository. Userspace mode avoids this host package dependency.
- acme.sh is downloaded at a fixed version and verified by SHA-256. Docker's
  signing key is verified by fingerprint.
- Public-address detection can query several public IP services. Prefix
  generation can query RIPE Stat. Failures are handled as feature-level errors.
- DiceBear and Gravatar avatars are optional and disabled by the default avatar
  settings. Enabling them permits the browser to contact those services.
- GitHub and GHCR are the official publication defaults, not protocol
  requirements. Another OCI registry and HTTPS artifact host can replace them.

See `NOTICE` and `LICENSE` for origin, attribution, and redistribution terms.
