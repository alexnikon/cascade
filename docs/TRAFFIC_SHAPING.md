# Traffic shaping

Cascade can apply per-peer bandwidth limits with Linux traffic control. Limits
are expressed from the client's perspective: download is traffic sent from the
VPN interface to the peer, and upload is traffic received from the peer.

## Download path

Download shaping uses an HTB hierarchy on the tunnel interface. Each limited
peer receives a deterministic class and a filter matching its tunnel address.
The class rate is derived from the peer or group policy.

## Upload path

Upload shaping uses an ingress qdisc and policing filter. Packets exceeding the
configured rate and burst allowance are dropped, allowing transport protocols to
reduce their sending rate.

## Class identity

Class IDs are derived deterministically from peer address data and checked for
collisions. They are runtime identifiers only; the peer ID remains the durable
application identity.

## Tunnel overhead

The configured payload rate and on-wire rate differ because WireGuard, UDP, and
IP headers add overhead. Cascade applies the configured policy consistently, but
operators should leave capacity headroom on constrained physical links. See
[../wireguard_chain_mtu_guide.md](../wireguard_chain_mtu_guide.md).

## Lifecycle

Rules are installed when an enabled peer with an effective rate becomes active.
They are replaced after relevant peer or group changes and removed when the peer
is disabled, deleted, moved, or no longer limited. Interface restart rebuilds the
required qdisc and filters from persisted policy.

## Diagnostics

Inspect download classes and filters:

```sh
tc -s class show dev wg10
tc -s filter show dev wg10
```

Inspect upload policing:

```sh
tc -s qdisc show dev wg10
tc -s filter show dev wg10 ingress
```

Packet, byte, overlimit, and drop counters distinguish an unused rule from an
active bottleneck. Also verify interface MTU, physical link capacity, and whether
another host or container qdisc is applying an additional limit.
