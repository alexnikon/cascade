"""Exercise the real Cascade binary and Caddy in an isolated privileged container."""
import json
import http.cookiejar
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import time
import urllib.request


def run(*args, check=True):
    return subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check)


def wait_http(url):
    for _ in range(100):
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                if response.status == 200:
                    return
        except Exception:
            time.sleep(0.1)
    raise AssertionError(f'Server did not start: {url}')


def direct(path, body=None, token=''):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request('http://127.0.0.1:8888/api' + path,
                                    data=None if body is None else json.dumps(body).encode(), headers=headers)
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def request(namespace, path, expected=200, token='', body=None, source='', host='198.51.100.10', spoof=False):
    authority = f'[{host}]' if ':' in host else host
    command = ['ip', 'netns', 'exec', namespace, 'curl', '-sS', '--cacert', '/certs/server.crt',
               '--connect-timeout', '1', '--max-time', '3', '--noproxy', '*', '-o', '/tmp/response-' + namespace,
               '-w', '%{http_code}', f'https://{authority}{path}']
    if token:
        command += ['-H', 'Authorization: Bearer ' + token]
    if body is not None:
        command += ['-H', 'Content-Type: application/json', '-d', json.dumps(body)]
    if source:
        command += ['--interface', source]
    if spoof:
        command += ['-H', 'X-Cascade-Client-IP: 10.8.2.2', '-H', 'X-Cascade-Proxy: 1', '-H', 'X-Forwarded-For: 10.8.2.2']
    response = run(*command, check=False)
    if expected == 0:
        assert response.returncode != 0, (namespace, path, response.stdout)
    else:
        assert response.returncode == 0 and response.stdout == str(expected), (namespace, path, response.stdout, response.stderr)
    return Path('/tmp/response-' + namespace).read_text() if response.returncode == 0 else ''


def main():
    if os.environ.get('CASCADE_WEB_ACCESS_TEST') != '1' or not Path('/fixture/Caddyfile').exists():
        raise SystemExit('Run only in the dedicated test container')
    urllib.request.install_opener(urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())))
    processes = []
    env = {**os.environ, 'BIND_ADDR': '127.0.0.1', 'PORT': '8888', 'CASCADE_PORT': '8888',
           'NETWORK_MODE': 'host', 'ADMIN_PATH': 'test-admin', 'WG_HOST': '198.51.100.10'}
    os.environ.update(env)
    for path in ('/state', '/certs', '/srv/www', '/logs', '/srv/acme/.well-known/acme-challenge'):
        Path(path).mkdir(parents=True, exist_ok=True)
    Path('/srv/www/index.html').write_text('public decoy')
    Path('/srv/acme/.well-known/acme-challenge/probe').write_text('acme proof')
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', '/certs/server.key',
        '-out', '/certs/server.crt', '-days', '2', '-subj', '/CN=localhost',
        '-addext', 'subjectAltName=IP:198.51.100.10,IP:10.8.2.1,IP:127.0.0.1,IP:fd00:beef::10,DNS:localhost')
    topology = [('admin', 'wg12', '10.8.2', 'fd00:12'), ('client', 'wg13', '10.10.1', 'fd00:13'),
                ('remote', 'wan-test', '203.0.113', 'fd00:14'), ('bot', 'br-bot', '172.18.0', 'fd00:15')]
    for namespace, device, subnet, v6 in topology:
        run('ip', 'netns', 'add', namespace)
        run('ip', 'link', 'add', device, 'type', 'veth', 'peer', 'name', 'peer-' + namespace)
        run('ip', 'link', 'set', 'peer-' + namespace, 'netns', namespace)
        run('ip', 'addr', 'add', subnet + '.1/24', 'dev', device)
        run('ip', '-6', 'addr', 'add', v6 + '::1/64', 'dev', device, 'nodad')
        run('ip', 'link', 'set', device, 'up')
        for args in (('link', 'set', 'lo', 'up'), ('link', 'set', 'peer-' + namespace, 'up'),
                     ('addr', 'add', subnet + '.2/24', 'dev', 'peer-' + namespace),
                     ('-6', 'addr', 'add', v6 + '::2/64', 'dev', 'peer-' + namespace, 'nodad'),
                     ('route', 'add', 'default', 'via', subnet + '.1'),
                     ('-6', 'route', 'add', 'default', 'via', v6 + '::1')):
            run('ip', '-n', namespace, *args)
    run('ip', 'addr', 'add', '198.51.100.10/32', 'dev', 'lo')
    run('ip', '-6', 'addr', 'add', 'fd00:beef::10/128', 'dev', 'lo', 'nodad')
    # The main application starts against an empty fixture DB. Simulated ingress
    # links are registered afterwards so tunnel startup never treats veth as WG.
    cascade_log = open('/tmp/cascade.log', 'w')
    cascade = subprocess.Popen(['cascade', '--data-dir', '/state'], env=env, stdout=cascade_log, stderr=cascade_log)
    processes.append(cascade)
    wait_http('http://127.0.0.1:8888/api/health')
    direct('/users', {'username': 'owner', 'password': 'local-test-password'})
    session = direct('/session', {'username': 'owner', 'password': 'local-test-password'})
    # Password auth is supported locally; only a token is used across Caddy.
    request_token = urllib.request.Request('http://127.0.0.1:8888/api/tokens',
        data=b'{"name":"local-e2e"}', headers={'Content-Type': 'application/json', 'Authorization': 'owner:local-test-password'})
    with urllib.request.urlopen(request_token) as response:
        token = json.load(response)['raw_token']
    con = sqlite3.connect('/state/cascade.db')
    con.execute("INSERT INTO interfaces(id,name,address,enabled) VALUES('wg12','admin','10.8.2.1/24, fd00:12::1/64',1)")
    con.execute("INSERT INTO interfaces(id,name,address,enabled) VALUES('wg13','clients','10.10.1.1/24',1)")
    con.execute("INSERT OR REPLACE INTO settings(key,value) VALUES('publicIPMode','manual'),('publicIPManual','198.51.100.10')")
    con.commit()
    # Public bot routes are deliberately outside the administrative gate.
    caddy = Path('/fixture/Caddyfile').read_text().replace('    # ── Decoy site', '''    handle /connect/* {
        respond "bot connect" 200
    }
    handle /webhook/yookassa* {
        respond "bot webhook" 200
    }
    # ── Decoy site''')
    Path('/tmp/Caddyfile').write_text(caddy)
    run('caddy', 'validate', '--config', '/tmp/Caddyfile', '--adapter', 'caddyfile', check=True)
    caddy_log = open('/tmp/caddy.log', 'w')
    processes.append(subprocess.Popen(['caddy', 'run', '--config', '/tmp/Caddyfile', '--adapter', 'caddyfile'], env=env, stdout=caddy_log, stderr=caddy_log))
    time.sleep(1)
    assert 'public decoy' in request('client', '/')
    policy = {'enabled': True, 'interfaceId': 'wg12', 'clients': [
        {'name': 'remote', 'ip': '203.0.113.2', 'interface': 'wan-test'},
        {'name': 'local bot', 'ip': '172.18.0.2', 'interface': 'br-bot'},
        {'name': 'remote IPv6', 'ip': 'fd00:14::2', 'interface': 'wan-test'}]}
    routes_before = run('ip', 'route', 'show').stdout
    forward_before = run('iptables-nft', '-S', 'FORWARD').stdout
    base = '/test-admin'
    request('admin', base + '/api/settings/web-access/trial', token=token, body=policy)
    request('admin', base + '/api/settings/web-access/confirm', token=token, body={})
    print('PASS: trial confirmed through administrative ingress')
    for host in ('198.51.100.10', '10.8.2.1', 'fd00:beef::10'):
        request('admin', base + '/', host=host)
        request('admin', base + '/api/session', host=host)
        request('admin', base + '/js/app.js', host=host)
        request('client', base + '/', 404, host=host, token=token, spoof=True)
        request('client', base + '/api/session', 404, host=host)
        request('client', base + '/api/health', 404, host=host, token=token)
    request('client', base, 404)
    print('PASS: other VPN denied by public IP, admin IP, IPv6, token, and forged headers')
    for namespace in ('bot', 'remote'):
        request(namespace, base + '/', 404, token=token)
        request(namespace, base + '/api/session', 404, token=token)
        request(namespace, base + '/api/health', 404)
        request(namespace, base + '/api/health', token=token)
        request(namespace, base + '/api/settings', token=token)
        request(namespace, base + '/api/settings/web-access/cancel', 403, token=token, body={})
        request(namespace, '/connect/example')
        request(namespace, '/webhook/yookassa')
    request('remote', base + '/api/health', token=token, host='fd00:beef::10')
    print('PASS: remote and Docker API require tokens and never expose the panel')
    for host in ('198.51.100.10', 'fd00:beef::10'):
        for namespace, path, credential, expected in (
            ('admin', base + '/', '', '200'),
            ('client', base + '/', token, '404'),
            ('remote', base + '/', token, '404'),
            ('remote', base + '/api/health', token, '200'),
            ('remote', base + '/api/health', '', '404'),
        ):
            response = run('ip', 'netns', 'exec', namespace, '/opt/http3/bin/python',
                           '/fixture/web_access_http3.py', host, path, credential)
            assert response.stdout.strip() == expected, (namespace, host, response.stdout)
    print('PASS: real HTTP/3 requests enforce the same policy over IPv4 and IPv6')
    migration = run('/opt/http3/bin/python', '/fixture/web_access_http3_migration.py')
    print(migration.stdout.strip())
    for namespace in ('admin', 'client', 'remote', 'bot'):
        request(namespace, '/cnf/0123456789abcdef0123456789abcdef', 404)
        assert 'public decoy' in request(namespace, '/')
    # Spoof an administrative source on a different ingress and prove the kernel
    # drops it before Caddy, for TCP and UDP (QUIC) in both IP families.
    run('ip', '-n', 'client', 'addr', 'add', '10.8.2.99/32', 'dev', 'peer-client')
    run('ip', '-n', 'client', '-6', 'addr', 'add', 'fd00:12::99/128', 'dev', 'peer-client', 'nodad')
    for host, source in (('198.51.100.10', '10.8.2.99'), ('fd00:beef::10', 'fd00:12::99')):
        request('client', base + '/', 0, source=source, host=host)
    for family, source, target in ((2, '10.8.2.99', '198.51.100.10'), (10, 'fd00:12::99', 'fd00:beef::10')):
        code = f'import socket; s=socket.socket({family},socket.SOCK_DGRAM); s.bind(("{source}",0)); s.sendto(b"quic probe",("{target}",443))'
        run('ip', 'netns', 'exec', 'client', 'python3', '-c', code)
    time.sleep(0.1)
    for binary in ('iptables-nft', 'ip6tables-nft'):
        counters = run(binary, '-nvxL', 'CASCADE_WEB_ACCESS').stdout
        dropped_udp = [line for line in counters.splitlines() if 'DROP' in line and 'udp' in line and int(line.split()[0]) > 0]
        assert dropped_udp, counters
    assert routes_before == run('ip', 'route', 'show').stdout
    assert forward_before == run('iptables-nft', '-S', 'FORWARD').stdout
    acme = run('ip', 'netns', 'exec', 'client', 'curl', '-fsS', 'http://198.51.100.10/.well-known/acme-challenge/probe').stdout
    assert acme == 'acme proof'
    print('PASS: ingress spoofing dropped for IPv4/IPv6 TCP and UDP; routes, FORWARD and ACME preserved')
    print('ALL NETWORK CHECKS PASSED')
    if os.environ.get('KEEP_TEST_CONTAINER') == '1':
        for i in range(1, 4):
            con.execute("INSERT INTO routes(id,description,destination,dev,enabled) VALUES(?,?,?,?,0)",
                        (f'review-route-{i}', f'Review route {i}', f'192.0.{i}.0/24', 'wg12'))
            con.execute("INSERT INTO nat_rules(id,name,source,out_interface,enabled) VALUES(?,?,?,?,0)",
                        (f'review-nat-{i}', f'Review NAT {i}', f'192.0.{i}.0/24', 'wan-test'))
            con.execute("INSERT INTO nat_dnat_rules(id,name,protocol,in_port,dest_ip,dest_port,enabled) VALUES(?,?,?,?,?,?,0)",
                        (f'review-forward-{i}', f'Review forward {i}', 'tcp', 10000+i, '192.0.2.10', 80,))
            con.execute("INSERT INTO aliases(id,name,type,entries) VALUES(?,?,?,?)",
                        (f'review-alias-{i}', f'review-network-{i}', 'network', json.dumps([f'192.0.{i}.0/24'])))
        con.commit()
        processes.append(subprocess.Popen(['ip', 'netns', 'exec', 'admin', 'python3', '/fixture/web_access_browser_proxy.py'], stdout=subprocess.DEVNULL, stderr=open('/tmp/browser-proxy.log','w')))
        processes.append(subprocess.Popen(['socat','TCP-LISTEN:9443,reuseaddr,fork','TCP:10.8.2.2:9444'], stdout=subprocess.DEVNULL, stderr=open('/tmp/socat.log','w')))
        print('Browser ready: http://localhost:18444/test-admin/ (owner / local-test-password); upstream TLS is verified')
        signal.pause()
    for process in reversed(processes):
        process.terminate()
    for process in reversed(processes):
        process.wait(timeout=15)


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError as error:
        print(error.stderr, flush=True)
        raise
