# Isolated metrics integration fixture; no Docker socket or host network needed.
FROM caddy:alpine
RUN apk add --no-cache iproute2 iptables ipset curl openssl python3 socat wireguard-tools
COPY cascade /usr/local/bin/cascade
COPY Caddyfile /fixture/Caddyfile
COPY metrics_e2e.py /fixture/metrics_e2e.py
CMD ["python3", "-u", "/fixture/metrics_e2e.py"]
