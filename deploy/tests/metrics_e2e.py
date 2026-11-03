"""Prove public metrics TLS/auth and VPN isolation with real Cascade and Caddy."""
import http.cookiejar
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time
import urllib.error
import urllib.request


def run(*args, check=True):
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def api(path, body=None, token='', method=None, expected=200):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = token
    request = urllib.request.Request('http://127.0.0.1:8888/api' + path,
        data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
    try:
        response = urllib.request.urlopen(request, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        payload = json.load(response)
        assert response.status == expected, (path, response.status, payload)
        return payload


def https(path='/metrics', token='metrics-secret', expected=200, extra=()):
    result = run('curl', '-sS', '--noproxy', '*', '--cacert', '/certs/server.crt',
        '--max-time', '5', '-o', '/tmp/metrics-response', '-w', '%{http_code}',
        '-H', 'Authorization: Bearer ' + token, *extra, 'https://198.51.100.10' + path)
    assert result.stdout == str(expected), (path, result.stdout, Path('/tmp/metrics-response').read_text())
    return Path('/tmp/metrics-response').read_text()


def http_metrics(address, expected=True):
    result = run('curl', '-fsS', '--noproxy', '*', '--max-time', '2',
        'http://' + address + ':9351/metrics', check=False)
    assert (result.returncode == 0) == expected, (address, result.returncode, result.stdout)
    if expected:
        assert 'cascade_build_info' in result.stdout


def wait_backend():
    for _ in range(100):
        try:
            api('/health')
            return
        except Exception:
            time.sleep(0.1)
    raise AssertionError('Cascade did not start')


run('ip', 'addr', 'add', '198.51.100.10/32', 'dev', 'lo')
for directory in ('/certs', '/logs', '/srv/www', '/srv/acme'):
    Path(directory).mkdir(parents=True, exist_ok=True)
Path('/srv/www/index.html').write_text('public decoy')
Path('/srv/www/404.html').write_text('not found')
run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
    '-nodes', '-days', '1', '-keyout', '/certs/server.key', '-out', '/certs/server.crt',
    '-subj', '/CN=metrics-test', '-addext', 'subjectAltName=IP:198.51.100.10')
Path('/tmp/Caddyfile').write_text(Path('/fixture/Caddyfile').read_text())

for mode in ('host', 'bridge'):
    processes = []
    logs = []
    prefix = []
    try:
        if mode == 'bridge':
            run('ip', 'netns', 'add', 'cascade-bridge')
            run('ip', 'link', 'add', 'bridge-host', 'type', 'veth', 'peer', 'name', 'bridge-peer')
            run('ip', 'link', 'set', 'bridge-peer', 'netns', 'cascade-bridge')
            run('ip', 'addr', 'add', '192.0.2.1/24', 'dev', 'bridge-host')
            run('ip', 'link', 'set', 'bridge-host', 'up')
            prefix = ['ip', 'netns', 'exec', 'cascade-bridge']
            run(*prefix, 'ip', 'link', 'set', 'bridge-peer', 'name', 'eth0')
            run(*prefix, 'ip', 'addr', 'add', '192.0.2.2/24', 'dev', 'eth0')
            run(*prefix, 'ip', 'link', 'set', 'eth0', 'up')
            run(*prefix, 'ip', 'link', 'set', 'lo', 'up')
            run(*prefix, 'ip', 'route', 'add', 'default', 'via', '192.0.2.1', 'dev', 'eth0')
            run('ip', 'route', 'add', '10.8.2.0/24', 'via', '192.0.2.2')
            processes.append(subprocess.Popen(['socat', 'TCP-LISTEN:8888,bind=127.0.0.1,reuseaddr,fork', 'TCP:192.0.2.2:8888']))
        env = dict(os.environ, BIND_ADDR='127.0.0.1' if mode == 'host' else '0.0.0.0',
            NETWORK_MODE=mode, WG_HOST='198.51.100.10', CASCADE_PORT='8888', ADMIN_PATH='test-admin')
        state = '/state-' + mode
        Path(state).mkdir()
        log = open('/tmp/cascade-' + mode + '.log', 'w')
        logs.append(log)
        processes.append(subprocess.Popen([*prefix, 'cascade', '--data-dir', state], env=env, stdout=log, stderr=log))
        urllib.request.install_opener(urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())))
        wait_backend()
        api('/users', {'username': 'owner', 'password': 'fixture-password'}, expected=201)
        api('/session', {'username': 'owner', 'password': 'fixture-password'})
        created = api('/tokens', {'name': 'metrics-test'}, token='owner:fixture-password', expected=201)
        admin_token = 'Bearer ' + created['raw_token']
        initial = api('/settings/metrics', token=admin_token)
        assert not initial['enabled'] and initial['configurationRequired']
        update = dict(enabled=True, mode='public_https', port=9351, historyEnabled=True)
        api('/settings/metrics', update, token=admin_token, method='PUT', expected=400)
        update['token'] = 'metrics-secret'
        state_response = api('/settings/metrics', update, token=admin_token, method='PUT')
        assert state_response['endpointUrl'] == 'https://198.51.100.10/metrics'
        log = open('/tmp/caddy-' + mode + '.log', 'w')
        logs.append(log)
        run('env', 'CASCADE_PORT=8888', 'ADMIN_PATH=test-admin', 'caddy', 'validate', '--config', '/tmp/Caddyfile', '--adapter', 'caddyfile')
        processes.append(subprocess.Popen(['caddy', 'run', '--config', '/tmp/Caddyfile', '--adapter', 'caddyfile'], env=env, stdout=log, stderr=log))
        time.sleep(0.8)
        assert 'cascade_build_info' in https()
        https(token='', expected=401)
        https(token='wrong', expected=401)
        https(token=created['raw_token'], expected=401)
        https('/test-admin/_internal/prometheus/metrics', expected=404,
            extra=('-H', 'X-Cascade-Metrics-Proxy: 1', '-H', 'X-Cascade-HTTPS-Port: 443'))
        untrusted = run('curl', '-sS', '--noproxy', '*', '--max-time', '2',
            'https://198.51.100.10/metrics', check=False)
        assert untrusted.returncode == 60, untrusted
        http_metrics('198.51.100.10', expected=False)
        # A selected interface binds only its address and automatically recovers.
        if mode == 'host':
            run('ip', 'netns', 'add', 'metrics-admin')
            run('ip', 'link', 'add', 'wg12', 'type', 'veth', 'peer', 'name', 'admin-peer')
            run('ip', 'link', 'set', 'admin-peer', 'netns', 'metrics-admin')
            run('ip', '-n', 'metrics-admin', 'addr', 'add', '10.8.2.2/24', 'dev', 'admin-peer')
            run('ip', '-n', 'metrics-admin', 'link', 'set', 'admin-peer', 'up')
            run('ip', '-n', 'metrics-admin', 'link', 'set', 'lo', 'up')
        else:
            run(*prefix, 'ip', 'link', 'add', 'wg12', 'type', 'dummy')
        run(*prefix, 'ip', 'addr', 'add', '10.8.2.1/24', 'dev', 'wg12')
        run(*prefix, 'ip', 'link', 'set', 'wg12', 'up')
        con = sqlite3.connect(state + '/cascade.db')
        con.execute("INSERT INTO interfaces(id,name,address,enabled) VALUES('wg12','metrics-vpn','10.8.2.1/24',1)")
        con.commit()
        update.pop('token')
        update.update(mode='vpn', interfaceId='wg12', clearToken=True)
        api('/settings/metrics', update, token=admin_token, method='PUT')
        https(expected=404)
        http_metrics('10.8.2.1')
        http_metrics('198.51.100.10', expected=False)
        run(*prefix, 'ip', 'link', 'set', 'wg12', 'down')
        time.sleep(1.3)
        status = api('/settings/metrics', token=admin_token)
        assert not status['listening'] and status['listenError'], status
        run(*prefix, 'ip', 'link', 'set', 'wg12', 'up')
        time.sleep(1.3)
        http_metrics('10.8.2.1')
        run(*prefix, 'ip', 'addr', 'del', '10.8.2.1/24', 'dev', 'wg12')
        run(*prefix, 'ip', 'addr', 'add', '10.8.2.3/24', 'dev', 'wg12')
        con.execute("UPDATE interfaces SET address='10.8.2.3/24' WHERE id='wg12'")
        con.commit()
        time.sleep(1.3)
        http_metrics('10.8.2.3')
        http_metrics('10.8.2.1', expected=False)
        update.update(mode='public_https', token='metrics-secret', clearToken=False)
        api('/settings/metrics', update, token=admin_token, method='PUT')
        http_metrics('10.8.2.3', expected=False)
        run(*prefix, 'ip', 'link', 'set', 'wg12', 'down')
        assert 'cascade_build_info' in https()
        if mode == 'bridge':
            run(*prefix, 'ip', 'route', 'del', 'default', 'via', '192.0.2.1', 'dev', 'eth0')
            assert 'cascade_build_info' in https()
        assert api('/settings/metrics', token=admin_token)['connectedPeerThresholdSeconds'] == 180
        if mode == 'host':
            run('ip', 'link', 'set', 'wg12', 'up')
            run('ip', '-n', 'metrics-admin', 'route', 'add', '198.51.100.10/32', 'via', '10.8.2.3')
            policy = {'enabled': True, 'interfaceId': 'wg12', 'clients': []}
            for action, body in [('trial', policy), ('confirm', {})]:
                result = run('ip', 'netns', 'exec', 'metrics-admin', 'curl', '-sS', '--noproxy', '*',
                    '--cacert', '/certs/server.crt', '--max-time', '5', '-o', '/tmp/policy-response', '-w', '%{http_code}',
                    '-H', 'Authorization: ' + admin_token, '-H', 'Content-Type: application/json',
                    '-d', json.dumps(body), 'https://198.51.100.10/test-admin/api/settings/web-access/' + action)
                assert result.stdout == '200', (action, result.stdout, Path('/tmp/policy-response').read_text())
            https('/test-admin/api/health', token=created['raw_token'], expected=404)
            run('ip', 'link', 'set', 'wg12', 'down')
            assert 'cascade_build_info' in https()
            print('PASS: VPN-only panel policy does not block public metrics with VPN down')
        print('PASS:', mode, 'TLS verification, auth, proxy isolation, mode switching, VPN recovery/address change, HTTPS with VPN down')
        con.close()
    except Exception:
        for log in logs:
            log.flush()
            print(Path(log.name).read_text()[-10000:])
        raise
    finally:
        for process in reversed(processes):
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        for log in logs:
            log.close()
        if mode == 'bridge':
            run('ip', 'netns', 'del', 'cascade-bridge', check=False)
            run('ip', 'link', 'del', 'bridge-host', check=False)
        else:
            run('ip', 'netns', 'del', 'metrics-admin', check=False)
            run('ip', 'link', 'del', 'wg12', check=False)
