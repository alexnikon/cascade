#!/usr/bin/env python3
"""Upgrade standard Cascade proxy rules while preserving unrelated Caddy routes."""
import argparse
import re
import subprocess
from pathlib import Path


OLD_PROXY = '        reverse_proxy 127.0.0.1:{$CASCADE_PORT}\n'
PREVIOUS_PROXY = '''        reverse_proxy 127.0.0.1:{$CASCADE_PORT} {
            # Always overwrite client-supplied policy metadata.
            header_up X-Cascade-Proxy "1"
            header_up X-Cascade-Client-IP {remote_host}
            header_up X-Cascade-HTTPS-Port {http.request.local.port}
        }
'''
NEW_PROXY = PREVIOUS_PROXY.replace('            # Always overwrite', '            header_up -X-Cascade-Metrics-Proxy\n            # Always overwrite')
METRICS_ROUTE = '''    # Cascade public metrics: Cascade checks access mode and bearer token.
    handle /metrics {
        reverse_proxy 127.0.0.1:{$CASCADE_PORT} {
            rewrite /_internal/prometheus/metrics
            header_up X-Cascade-Metrics-Proxy "1"
            header_up X-Cascade-HTTPS-Port {http.request.local.port}
        }
    }

'''
OLD_LINK = '''    # One-time peer config download links — public, no admin path prefix.
    handle /cnf/* {
        reverse_proxy 127.0.0.1:{$CASCADE_PORT}
    }

'''
OLD_REDIRECT = '    redir /{$ADMIN_PATH} /{$ADMIN_PATH}/ 301\n'
NEW_REDIRECT = '''    handle /{$ADMIN_PATH} {
        reverse_proxy 127.0.0.1:{$CASCADE_PORT} {
            rewrite /_web-access/redirect
            header_up X-Cascade-Proxy "1"
            header_up X-Cascade-Client-IP {remote_host}
            header_up X-Cascade-HTTPS-Port {http.request.local.port}
        }
    }
'''


def upgrade(content: str) -> str:
    content = content.replace(OLD_LINK, '')
    if 'handle /cnf/' in content:
        raise ValueError('Customized /cnf route: remove it manually before upgrading')
    content = content.replace(PREVIOUS_PROXY, NEW_PROXY)
    if NEW_PROXY not in content:
        if content.count(OLD_PROXY) != 1:
            raise ValueError('Customized administrative proxy: apply the documented headers manually')
        content = content.replace(OLD_PROXY, NEW_PROXY, 1)
    if OLD_REDIRECT in content:
        content = content.replace(OLD_REDIRECT, NEW_REDIRECT, 1)
    if 'rewrite /_web-access/redirect' not in content:
        raise ValueError('Customized administrative redirect: update it manually')
    if METRICS_ROUTE not in content:
        if re.search(r'(?m)^[^#\n]*[ \t]/metrics(?:[/* \t{]|$)', content) or '/_internal/prometheus/metrics' in content:
            raise ValueError('Customized metrics route: review it manually before upgrading')
        marker = '    # ── Admin interface'
        if content.count(marker) != 1:
            raise ValueError('Cannot identify the standard HTTPS route insertion point')
        content = content.replace(marker, METRICS_ROUTE + marker, 1)
    return content


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('caddyfile', type=Path)
    parser.add_argument('--check', action='store_true', help='Validate without writing')
    args = parser.parse_args()
    original = args.caddyfile.read_text()
    updated = upgrade(original)
    if args.check:
        print('Ready' if updated == original else 'Upgrade required')
        return
    if updated != original:
        # Validate inside the running proxy, where its environment and TLS
        # certificate mounts are available. Failure leaves the file untouched.
        subprocess.run(['docker', 'exec', '-i', 'cascade-caddy', 'caddy', 'validate',
                        '--config', '/dev/stdin', '--adapter', 'caddyfile'],
                       input=updated, text=True, check=True)
        backup = args.caddyfile.with_name(args.caddyfile.name + '.before-metrics-access')
        if backup.exists():
            raise SystemExit(f'Backup already exists: {backup}')
        backup.write_text(original)
        # Preserve the inode: Docker may have this exact file bind-mounted.
        with args.caddyfile.open('w') as target:
            target.write(updated)
        print(f'Updated; backup: {backup}. Caddy configuration validated. Restart only cascade-caddy to activate it.')


if __name__ == '__main__':
    main()
