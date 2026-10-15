# Build context is assembled by scripts/test-web-access.sh in a temporary directory.
FROM caddy:alpine
RUN apk add --no-cache iproute2 iptables ipset curl openssl python3 py3-pip socat wireguard-tools bash && \
    python3 -m venv /opt/http3 && /opt/http3/bin/pip install --no-cache-dir aioquic==1.3.0
COPY cascade /usr/local/bin/cascade
COPY Caddyfile /fixture/Caddyfile
COPY web_access_e2e.py /fixture/web_access_e2e.py
COPY web_access_http3.py /fixture/web_access_http3.py
COPY web_access_http3_migration.py /fixture/web_access_http3_migration.py
COPY web_access_browser_proxy.py /fixture/web_access_browser_proxy.py
ENV CASCADE_WEB_ACCESS_TEST=1
CMD ["python3", "-u", "/fixture/web_access_e2e.py"]
