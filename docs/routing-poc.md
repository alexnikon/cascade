# Split-routing proof of concept

This historical proof of concept validated policy routing in which domestic
prefixes leave directly through one server while other VPN client traffic is
forwarded through a second server.

## Reference topology

```text
VPN client -> primary server -> domestic destination
                         `----> inter-server tunnel -> secondary egress
```

The primary server classifies destination prefixes with an ipset, marks
non-domestic client traffic, and selects a dedicated routing table. The secondary
server has a return route to the client subnet and performs egress masquerade.

## Primary-server outline

```sh
ipset create domestic hash:net family inet
ip rule add fwmark 0x1 lookup 100
ip route replace default dev <inter-server-interface> table 100
```

Firewall policy marks only client-subnet traffic whose destination is not in the
domestic set. Management, tunnel endpoints, and private/local networks require
explicit bypass rules before the general mark rule.

## Secondary-server outline

```sh
ip route replace <client-subnet> dev <inter-server-interface>
iptables-nft -t nat -A POSTROUTING -s <client-subnet> -o <public-interface> -j MASQUERADE
```

The production implementation should use Cascade's persistent routing, alias,
gateway, and NAT models rather than applying these commands manually.

## Findings

- A source-only masquerade rule on the primary client interface is too broad and
  may rewrite traffic before policy routing sends it to the secondary server.
- Mixing legacy iptables and nft-backed iptables makes counters and behavior
  appear inconsistent. Use the same `iptables-nft` backend as Cascade.
- The secondary server needs an explicit return route to the original client
  subnet unless the primary server intentionally hides it with NAT.
- Policy-table failure needs an explicit fallback policy to avoid traffic leaks.
- Prefix refresh must be atomic so a partial list cannot temporarily reroute a
  large portion of traffic.

## Diagnostics

```sh
ipset list domestic
ip rule show
ip route show table 100
iptables-nft -t mangle -L -n -v
iptables-nft -t nat -L POSTROUTING -n -v
ip route get <destination> mark 1
wg show
```

Validate from a client with both domestic and foreign targets and compare the
observed public address with the expected egress. Packet counters should advance
only on the rule and server selected for each target.

For the maintained workflow, use
[SPLIT_ROUTING_RUSSIA_NETHERLANDS.md](SPLIT_ROUTING_RUSSIA_NETHERLANDS.md).
