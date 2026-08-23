# Split routing between Russia and the Netherlands

This guide routes domestic destinations directly from a Russian Cascade server
and sends other client traffic through a site-to-site tunnel to a Netherlands
Cascade server.

## Topology

```text
Client -> RF client tunnel -> RF policy routing
                              |-> domestic prefixes -> RF uplink
                              `-> other prefixes -> RF/NL S2S -> NL uplink
```

Use addresses and ports appropriate for your environment. Keep management access
outside the policy being changed so an incorrect route cannot lock you out.

## 1. Register the remote server

On the RF server, add the NL Cascade instance under Remotes and verify API
connectivity. Use a scoped token and trusted TLS. The remote integration is a
management path; the S2S tunnel still carries data-plane traffic.

## 2. Configure the RF server

### Client interface

Create a client-facing WireGuard or AmneziaWG interface. Allocate a subnet that
does not overlap either server, the S2S link, or client LANs. Enable normal
client routes and create no broad rule that forces all traffic through the RF
uplink before policy routing can classify it.

### S2S interface

Create an interconnect interface with automatic routes disabled. Allocate a
small dedicated transit subnet. Create the peer representing the NL server and
include the networks that must traverse the link.

If AmneziaWG is used, both ends must use compatible protocol versions and
matching obfuscation parameters. AWG 2.0 profiles can be exchanged through the
export/import workflow; verify every parameter before start.

### RF NAT

Masquerade client traffic only when it exits the intended RF uplink. Avoid a
broad source-only masquerade rule on the client tunnel because it can rewrite
traffic before it enters the S2S path.

## 3. Configure the NL server

Create the reciprocal S2S interface and peer with the same transit network and
compatible protocol settings. Add a route back to the RF client subnet through
the S2S interface.

Enable forwarding between the S2S interface and the NL uplink. Masquerade the RF
client subnet only on the NL public uplink. Preserve management and local server
traffic outside this rule.

## 4. Configure RF policy routing

### Gateway

Create a gateway using the NL transit address through the S2S interface. Add a
health check that tests the path without depending on a destination blocked by
the provider. Wait for stable healthy state before binding client policy to it.

### Domestic-prefix alias

Create or import a maintained alias containing domestic IPv4 and IPv6 prefixes.
Treat the source as an external operational dependency: pin or validate fetched
data and preserve the last known-good set when refresh fails.

### Rules

Order rules from specific to general:

1. Preserve access to Cascade management, tunnel endpoints, and local networks.
2. Send domestic-prefix alias destinations through the RF uplink.
3. Send remaining client-subnet traffic through the NL gateway/table.
4. Use an explicit safe fallback when the NL gateway is down; do not silently
   leak traffic through an unintended default route.

## 5. Add and test a client

Create one client before bulk migration. Validate:

```sh
ping <rf-tunnel-gateway>
curl https://ifconfig.me
```

Test at least one domestic and one foreign destination. Verify the selected
route on the RF server, handshake and byte counters on both S2S peers, and NAT
counters on the expected egress servers.

## Troubleshooting

- No S2S handshake: check endpoint, published UDP port, protocol parameters,
  keys, and host firewall.
- Handshake without traffic: check reciprocal allowed IPs and the NL return
  route.
- All traffic exits RF: check rule order, alias membership, packet marks, and
  policy-table default route.
- All traffic exits NL: check the domestic alias and the specific RF rule.
- Intermittent leaks: verify gateway failover behavior and remove competing
  default routes from the policy table.
- Small requests work but large transfers stall: validate path MTU and MSS
  clamping using [../wireguard_chain_mtu_guide.md](../wireguard_chain_mtu_guide.md).
