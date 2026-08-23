# Docker deployment modes

## Overview

Cascade supports three network layouts. Choose the layout based on isolation,
port-publication, and host-routing requirements rather than image registry or CI
provider.

## Host network

`network_mode: host` gives the container direct access to host interfaces and
ports. It is the simplest mode for VPN routing and has the least translation
between Cascade and the kernel.

Advantages:

- No Docker UDP port range publication.
- Host interface and route visibility is direct.
- Lowest networking complexity.

Tradeoffs:

- Weaker network-namespace isolation.
- UI and VPN ports share the host namespace.
- Port conflicts must be managed on the host.

## Bridge network

The standard Docker bridge isolates the container namespace. Publish the UI TCP
port and every UDP port available to Cascade's interface port pool.

Advantages:

- Familiar Docker isolation and explicit exposure.
- Easier coexistence with services using host networking.

Tradeoffs:

- UDP range and application port-pool configuration must match.
- Host-to-container NAT adds another troubleshooting layer.
- Host routes and policy may require explicit integration.

See [DEPLOY_BRIDGE_USERSPACE.md](DEPLOY_BRIDGE_USERSPACE.md).

## Isolated or OVS network

`network_mode: none` starts with no normal Docker interface. Deployment tooling
attaches a veth or OVS port, assigns addresses and routes, and repeats attachment
after container replacement.

Advantages:

- Maximum control over topology and segmentation.
- Suitable for lab fabrics, OVS policy, and custom namespaces.

Tradeoffs:

- Attachment and cleanup are deployment responsibilities.
- Restart ordering and idempotency are critical.
- Public endpoint discovery must use an explicit configured host address.

## Comparison

| Property | Host | Bridge | Isolated/OVS |
| --- | --- | --- | --- |
| Network isolation | Low | Medium | High |
| UDP publication | Not required | Required range | Fabric-specific |
| Setup complexity | Low | Medium | High |
| Host route visibility | Direct | Namespace-dependent | Explicit |
| Reattach after replace | No | No | Yes |

## Deployment contract

Portable deployment uses:

- `CASCADE_RELEASE_BASE_URL` for release artifacts.
- an exact release image tag in the bundled Compose file.
- checksummed release archives and installer assets.
- Compose rendering before restart.
- health and actual-image verification after restart.

GitHub repository coordinates are compatibility shorthand only. Any HTTPS object
store and any OCI registry can satisfy the deployment contract.

## Open operational questions

- Which mode is the supported default for each published bundle?
- Who owns host firewall changes and persisted sysctls?
- How is an isolated container reattached after runtime restart?
- Which host address is embedded in client configurations behind NAT?

Record answers in deployment-specific configuration, not application defaults.
