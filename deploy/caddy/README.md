# Cascade — Caddy Reverse Proxy

Caddy sits in front of Cascade and provides:
- HTTPS + HTTP/3 (QUIC) on port 443
- Decoy streaming site on `/`
- Hidden admin path (configured via `ADMIN_PATH` env var)
- Rate limiting on login endpoint (5 attempts / IP / minute)
- Security headers (HSTS, no-referrer, X-Frame-Options, …)
- TLS cert for bare public IP via acme.sh shortlived profile

## Quick start

### 1. Issue TLS certificate

```bash
# Port 80 must be reachable from the internet (acme.sh standalone mode binds it briefly).
# No existing HTTP server needed — this is designed to run BEFORE Caddy starts.
chmod +x scripts/acme-install.sh
sudo ./scripts/acme-install.sh <YOUR_PUBLIC_IP> <YOUR_EMAIL>
```

### 2. Configure

```bash
# Edit ../../.env and set ADMIN_PATH to a random string.
# Generate one: openssl rand -hex 12
```

### 3. Generate decoy video

`setup.sh` generates a 60-second noise video automatically via ffmpeg during step 5a.
If running Caddy manually without `setup.sh`, create the file yourself:

```bash
# Option A — ffmpeg (generates a noise clip; no copyrighted content)
ffmpeg -f lavfi -i color=c=black:s=1280x720:r=25 \
       -f lavfi -i anoisesrc=r=44100 \
       -t 60 -c:v libx264 -c:a aac \
       www/video/decoy.mp4

# Option B — download a royalty-free sample
# curl -L "https://..." -o www/video/decoy.mp4
```

### 4. Ensure Cascade binds to 127.0.0.1 only

Set `BIND_ADDR=127.0.0.1` in `docker-compose.yml` (already the default).
This prevents Cascade from being reachable directly from the internet — all traffic
must go through Caddy's hidden `ADMIN_PATH`.

As a second layer, block the port via iptables:
```bash
iptables-nft -A INPUT ! -i lo -p tcp --dport 8888 -j DROP
```

### 5. Start Caddy

```bash
docker compose up -d
```

### 6. Access admin interface

```
https://<IP>/<ADMIN_PATH>/
```

## Security notes

- `ADMIN_PATH` is security through obscurity — enable TOTP 2FA in Settings → Users for a real second factor
- `Referrer-Policy: no-referrer` prevents the hidden path from leaking via Referer headers
- Rate limiting blocks brute force on the login endpoint (5 POST /api/session per IP per minute)
- Cascade port (default 8888) MUST NOT be reachable from the internet (see step 4)
- TLS cert renews automatically every 3 days via acme.sh cron (webroot via Caddy after first issue)
- Caddy container runs read-only with minimal capabilities (NET_BIND_SERVICE only)

## Certificate issuance model

**First issuance** (`acme-install.sh`): uses acme.sh `--standalone` mode.
acme.sh temporarily binds port 80, answers the ACME HTTP-01 challenge, then exits.
No existing HTTP server required — this is intentional (Caddy can't start without a cert).

**Renewals** (automatic, every 3 days via cron): acme.sh switches to webroot mode,
placing the challenge token in `/srv/acme`. Caddy serves `/.well-known/acme-challenge/*`
from that directory (configured in `Caddyfile`), then gets reloaded automatically.

```
First time:  acme.sh --standalone   (binds :80 itself, no Caddy needed)
Renewals:    acme.sh --webroot /srv/acme  (Caddy serves the challenge)
```

## Browser sign-in and HTTPS over VPN

Cascade keeps browser sessions in SQLite for 400 days and renews them while the panel is in use, including background polling. Sessions survive restarts and upgrades as long as the data volume is preserved. You can review and revoke sign-ins in **Settings → Active sessions**. Password and 2FA changes revoke other browser sessions; restoring a backup clears all restored sessions. Upgrading from the previous in-memory store requires one fresh sign-in.

A VPN encrypts packets inside its tunnel, but the browser still classifies an `http://` page as HTTP. Cascade cannot remove the browser's “Not secure” indicator or make HTTP password fields a secure context. To avoid warnings, use HTTPS with a certificate that the browser trusts and that matches the exact hostname or IP in the URL.

Choose one of these approaches:

1. **Keep the existing certified HTTPS address.** Open `https://<CERTIFIED_IP>/<ADMIN_PATH>/` and route that destination through the administrative VPN. For split-tunnel clients this usually means including the certified server IP as a `/32` route (or `/128` for IPv6) in the appropriate VPN profile. Verify the route with your network layout before changing it: the route to the VPN's own transport endpoint must continue to work. When VPN-only web access is enabled, the request must arrive through the selected administrative VPN with an allowed source address.
2. **Use a domain over the VPN.** Issue a trusted certificate covering a hostname such as `cascade.example.com`, configure Caddy to serve that certificate, and resolve the hostname to Cascade's reachable VPN address using VPN DNS or a local hosts entry. Open `https://cascade.example.com/<ADMIN_PATH>/`. The certificate must cover `cascade.example.com`; an existing certificate covering only the public IP does not cover this name. DNS-based certificate validation can support a service that is reachable only through a VPN.

Opening `https://<PRIVATE_VPN_IP>/` with a certificate issued for the public IP or a different hostname produces a certificate mismatch. A self-signed certificate also produces warnings unless its issuing authority is installed and trusted on each client. Prefer one stable, correctly certified address for daily use.

HTTP sign-in remains supported through the VPN. HTTP and HTTPS use separate cookies, so signing in or out on one transport does not overwrite the other. Different IPs, hostnames, and browser profiles also have separate sign-ins; authorization is not automatically copied between them.

The 400-day lifetime is a practical cookie retention ceiling, not a promise that every browser will retain a cookie for that long. Chrome caps explicit expiry at 400 days; browsers may remove cookies sooner, and clearing site data or ending a private browsing session can require another sign-in.

References: [browser secure contexts](https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Secure_Contexts), [Chrome cookie expiry limit](https://developer.chrome.com/blog/cookie-max-age-expires).
