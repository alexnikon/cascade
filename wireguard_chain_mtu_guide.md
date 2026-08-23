# MTU and MSS in chained WireGuard tunnels

## Concepts

MTU is the largest IP packet an interface can carry without fragmentation. TCP
MSS is the largest TCP payload in a segment. WireGuard wraps an inner packet in
UDP and an outer IP header, reducing the available inner MTU. Chaining tunnels
repeats this overhead and may also encounter a smaller underlay path MTU.

MSS clamping adjusts TCP SYN advertisements so endpoints avoid creating packets
too large for the tunnel path. It does not fix oversized UDP or non-TCP traffic.

## Calculation

Start with the smallest verified underlay path MTU. A typical WireGuard overhead
budget is about 60 bytes over IPv4 and 80 bytes over IPv6. Platform details and
additional encapsulation can require more headroom.

For a chain, calculate from the outside inward:

1. Determine the physical/path MTU.
2. Subtract outer tunnel overhead for the outer interface.
3. Subtract the next tunnel overhead for the inner interface.
4. Derive TCP MSS from the resulting inner MTU: subtract 40 bytes for IPv4 TCP
   headers or 60 bytes for IPv6 TCP headers without options.
5. Verify with real traffic and reduce conservatively if the path still drops
   large packets.

Example with a 1500-byte IPv4 underlay and two conservative 60-byte layers:

| Layer | MTU |
| --- | ---: |
| Physical path | 1500 |
| Outer WireGuard | 1440 |
| Inner WireGuard | 1380 |
| Suggested IPv4 TCP MSS ceiling | 1340 |

Do not treat the example as universal. PPPoE, VLANs, IPv6 outer transport,
cloud overlays, and provider filtering change the available budget.

## Configuration

Set interface MTU explicitly when automatic detection cannot see the complete
chain. Cascade supports global and per-interface MTU, with the interface value
taking precedence.

Linux forwarding example:

```sh
iptables-nft -t mangle -A FORWARD -o wg10 -p tcp --tcp-flags SYN,RST SYN \
  -j TCPMSS --clamp-mss-to-pmtu
iptables-nft -t mangle -A FORWARD -i wg10 -p tcp --tcp-flags SYN,RST SYN \
  -j TCPMSS --clamp-mss-to-pmtu
```

Locally generated server traffic may require an OUTPUT-chain rule. On
OPNsense/pfSense, apply the equivalent normalization or MSS setting on the
relevant tunnel/firewall rule.

## Measuring path MTU

Linux IPv4 probes use a payload smaller than packet MTU by the IP and ICMP header
size:

```sh
ping -M do -s 1372 <destination>
```

On macOS/BSD, use the platform's do-not-fragment option and adjust payload size.
Binary-search the largest reliable size. Test both directions when possible,
because asymmetric paths can differ.

Inspect configured MTUs:

```sh
ip link show
```

## AmneziaWG considerations

AmneziaWG junk and handshake obfuscation parameters change control traffic and
traffic appearance. Data-packet encapsulation still requires WireGuard-style
MTU planning, while additional network layers or implementations may justify
extra margin. Never assume that a successful handshake proves large data packets
can traverse the path.

## Common symptoms

- Handshake succeeds but websites stall on larger responses.
- ICMP and small requests work while downloads freeze.
- TCP improves after MSS clamping but UDP applications still fail.
- Only one direction has throughput problems.

When these occur, inspect tunnel counters and firewall drops, test decreasing MTU
on the innermost interface, validate both path directions, and check whether an
intermediate NAT or overlay adds unaccounted encapsulation.
