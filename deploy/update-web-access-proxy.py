#!/usr/bin/env python3
"""Upgrade standard Cascade proxy rules while preserving unrelated Caddy routes."""
import argparse
from pathlib import Path


OLD_PROXY = '        reverse_proxy 127.0.0.1:{$CASCADE_PORT}\n'
NEW_PROXY = '''        reverse_proxy 127.0.0.1:{$CASCADE_PORT} {
            # Always overwrite client-supplied policy metadata.
            header_up X-Cascade-Proxy "1"
            header_up X-Cascade-Client-IP {remote_host}
            header_up X-Cascade-HTTPS-Port {http.request.local.port}
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
    if NEW_PROXY not in content:
        if content.count(OLD_PROXY) != 1:
            raise ValueError('Customized administrative proxy: apply the documented headers manually')
        content = content.replace(OLD_PROXY, NEW_PROXY, 1)
    if OLD_REDIRECT in content:
        content = content.replace(OLD_REDIRECT, NEW_REDIRECT, 1)
    if 'rewrite /_web-access/redirect' not in content:
        raise ValueError('Customized administrative redirect: update it manually')
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
        backup = args.caddyfile.with_name(args.caddyfile.name + '.before-web-access')
        if backup.exists():
            raise SystemExit(f'Backup already exists: {backup}')
        backup.write_text(original)
        # Preserve the inode: Docker may have this exact file bind-mounted.
        with args.caddyfile.open('w') as target:
            target.write(updated)
        print(f'Updated; backup: {backup}. Validate Caddy before restarting it.')


if __name__ == '__main__':
    main()
