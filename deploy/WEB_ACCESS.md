# Administrative VPN access

Settings → Web access can restrict the Cascade panel to one managed WireGuard or
AmneziaWG interface. Other VPN interfaces, including S2S links, cannot open the
panel. The restriction covers login, static assets, API requests, and existing
HTTP connections on subsequent requests.

This feature requires Linux host networking, a loopback backend bind, and the
updated standard Caddy configuration on TCP/UDP 443. It is disabled by default.
The public decoy, ACME HTTP-01 challenge on port 80, and independently configured
wgbot `/connect/*` and `/webhook/yookassa*` routes remain public.

## Upgrade the proxy

Updating only the Cascade image does not update an existing host Caddyfile.
Use the helper against the existing file so custom routes are preserved:

```sh
python3 deploy/update-web-access-proxy.py deploy/caddy/Caddyfile --check
python3 deploy/update-web-access-proxy.py deploy/caddy/Caddyfile
docker exec cascade-caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker restart cascade-caddy
```

The helper creates `Caddyfile.before-web-access`, preserves the bind-mounted
file's inode, removes the retired `/cnf/*` proxy, and updates only the standard
administrative proxy and redirect. It refuses customized administrative rules
instead of overwriting them. For a customized proxy, apply the administrative
blocks from the supplied Caddyfile manually while keeping unrelated routes.

Caddy overwrites `X-Cascade-Proxy`, `X-Cascade-Client-IP`, and
`X-Cascade-HTTPS-Port` on every administrative request. Cascade accepts this
metadata only from a loopback connection and port 443. Do not expose the backend
port or place another unconfigured proxy between clients and this Caddy instance.

## Configure access

1. Connect through the administrative VPN. Open the existing HTTPS address and
   sign in as an administrator.
2. Select exactly one active VPN interface. Its configured IPv4/IPv6 subnets must
   match the addresses installed on the running interface.
3. Add each required API client using its exact source IP and incoming interface.
   Every exception also requires a valid Bearer API token. It does not permit
   panel access, browser login, or password-based remote onboarding.
4. Apply the 120-second trial. Confirm through the selected VPN before its
   deadline. Otherwise the previous confirmed policy is restored automatically.
   A process restart during the trial also restores the previous policy.

For a bare-IP certificate, keep the same HTTPS URL and route that public IP
through the administrative VPN using the client's AllowedIPs. For a domain,
split DNS is also possible, retaining the certificate's original hostname.
Opening a private IP with a certificate issued for a public IP will not validate.

The administrative source subnet is paired with its incoming VPN interface by
dedicated INPUT guards. Connecting to the server's public IP from a different
VPN does not bypass the restriction. The guards cover TCP and UDP/HTTP3 in both
IP families and do not change FORWARD, NAT, PBR, or S2S routes. Spoofing an
administrative or trusted-machine source through another interface is dropped.

The selected administrative interface cannot be deleted, stopped, or changed in
a way that restarts it while the policy is active or pending. Change the web
policy first. If the interface disappears, the panel is not reopened publicly.
If startup cannot restore the web policy or its kernel guards, HTTP access stays
closed while the remaining VPN and S2S services continue their normal startup.

## wgbot and remote servers

wgbot downloads configurations using the authorized
`/api/tunnel-interfaces/{id}/peers/{peerId}/config` endpoint. It does not need
public configuration links. One-time link generation, redemption, and UI controls
have been removed globally; existing tokens are cleared by migration.

For servers managed over the Internet, allow the actual public egress address of
each managing Cascade server or wgbot host on its WAN ingress interface. Existing
HTTPS URLs and API tokens continue to work. Add new remote servers using the
explicit-token mode; obtain the token through the target's administrative VPN.

A local Docker container may reach Caddy through a different path. On the
inspected vpnbox deployment, wgbot reaches local Caddy with source `172.18.0.2`
through `br-2137d4bb4f1b`, while remote Cascade servers see `222.167.238.53` after
NAT. These are deployment-specific examples, not defaults. Verify them before
use. Allow the exact container IP and bridge, not the whole Docker subnet. If
Docker changes either identity, update the exception explicitly.

## SSH recovery

Reset the confirmed policy through the local CLI, then restart Cascade:

```sh
docker exec cascade /usr/local/bin/cascade --reset-web-access --data-dir /etc/wireguard/data
docker restart cascade
```

The command changes the stored policy. The running process keeps its current
restriction until restarted. Startup restores the confirmed policy and rebuilds
its kernel guards before opening the HTTP listener.

## API

All paths below are under the existing administrative `/api` base URL:

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/settings/web-access` | Policy, pending deadline/error, managed VPN interfaces, proxy support, and management permission |
| POST | `/settings/web-access/trial` | Trial `{enabled, interfaceId, clients: [{name, ip, interface}]}` for 120 seconds |
| POST | `/settings/web-access/confirm` | Persist the trial; requires administrator access through the candidate VPN, or the previous VPN when disabling |
| POST | `/settings/web-access/cancel` | Restore the previous confirmed policy |

Machine API exceptions cannot change the web policy. Policy writes require an
existing administrator account. The normal settings PUT endpoint does not alter
this policy.

## Local validation

```sh
go test ./... -count=1 -timeout 180s
go test -race ./internal/webaccess ./internal/api -run 'Test(WebAccess|Origin|TrialRestart|DisableTrial|KernelFailure|ProtectedInterface|InvalidPolicies)' -count=1
python3 deploy/tests/web_access_proxy_test.py
bash scripts/test-web-access.sh
```

The Docker test uses a dedicated privileged container without host networking.
It runs the real binary and Caddy, creates independent administrative, client,
remote-server, and bot network namespaces, and verifies TLS with a temporary test
CA certificate. Production hosts and their firewall rules are not accessed.
