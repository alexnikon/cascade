#!/bin/bash
# tcp_tune.sh — automatic TCP buffer sizing and VPN tuning
# Requires: bash 4+, awk, grep, sysctl, tc (iproute2)
# Target platform: Ubuntu 22.04+ (kernel 5.15+)

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[ OK ]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
skip()  { echo -e "  ${YELLOW}SKIP${NC}  $*"; }
die()   { echo -e "${RED}[ERR ]${NC} $*" >&2; exit 1; }

# ── Environment checks ─────────────────────────────────────────────────────
[[ $EUID -ne 0 ]] && die "Root privileges are required"

for _cmd in awk grep sysctl tc; do
    command -v "$_cmd" >/dev/null 2>&1 || die "Required command not found: '$_cmd'. Install iproute2 / procps."
done

[[ -f /proc/meminfo ]] || die "/proc/meminfo is unavailable — this script requires Linux with procfs"

# ── RAM (pure Bash, no bc) ──────────────────────────────────────────────────
RAM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
RAM_MB=$(( RAM_KB / 1024 ))
RAM_GB=$(( RAM_MB / 1024 ))
RAM_GB_FRAC=$(( (RAM_MB % 1024) * 10 / 1024 ))   # One decimal place
# Number of memory pages (4 KB each)
RAM_PAGES=$(( RAM_KB / 4 ))

info "RAM: ${RAM_MB} MB (${RAM_GB}.${RAM_GB_FRAC} GB) = ${RAM_PAGES} pages"

# ── RAM tiers ──────────────────────────────────────────────────────────────
# tcp_mem: min / pressure / max, in pages
# max ≈ 25% RAM, pressure ≈ 75% max, min ≈ 50% pressure
# Scale core/TCP buffers with RAM, subject to reasonable upper limits.

if [[ $RAM_MB -lt 512 ]]; then
    TIER="tiny (<512MB)"
    CORE_RMAX=$(( 512 * 1024 ))
    CORE_WMAX=$(( 512 * 1024 ))
    CORE_RDEF=$(( 64  * 1024 ))
    CORE_WDEF=$(( 64  * 1024 ))
    TCP_RMIN=4096; TCP_RDEF=$(( 32 * 1024 )); TCP_RMAX=$(( 512 * 1024 ))
    TCP_WMIN=4096; TCP_WDEF=$(( 16 * 1024 )); TCP_WMAX=$(( 512 * 1024 ))
    # UDP buffers for WireGuard (UDP-first VPN)
    UDP_RMIN=4096; UDP_RDEF=$(( 64  * 1024 )); UDP_RMAX=$(( 512 * 1024 ))
    BACKLOG=256; SOMAXCONN=512; TW_BUCKETS=8192; NETDEV_BACKLOG=1000
    MAX_ORPHANS=8192

elif [[ $RAM_MB -lt 1024 ]]; then
    TIER="small (512MB–1GB)"
    CORE_RMAX=$(( 4  * 1024 * 1024 ))
    CORE_WMAX=$(( 4  * 1024 * 1024 ))
    CORE_RDEF=$(( 128 * 1024 ))
    CORE_WDEF=$(( 128 * 1024 ))
    TCP_RMIN=4096; TCP_RDEF=$(( 65 * 1024 )); TCP_RMAX=$(( 4 * 1024 * 1024 ))
    TCP_WMIN=4096; TCP_WDEF=$(( 32 * 1024 )); TCP_WMAX=$(( 4 * 1024 * 1024 ))
    UDP_RMIN=4096; UDP_RDEF=$(( 256 * 1024 )); UDP_RMAX=$(( 4 * 1024 * 1024 ))
    BACKLOG=512; SOMAXCONN=1024; TW_BUCKETS=32768; NETDEV_BACKLOG=2000
    MAX_ORPHANS=16384

elif [[ $RAM_MB -lt 2048 ]]; then
    TIER="medium (1–2GB)"
    CORE_RMAX=$(( 8  * 1024 * 1024 ))
    CORE_WMAX=$(( 8  * 1024 * 1024 ))
    CORE_RDEF=$(( 256 * 1024 ))
    CORE_WDEF=$(( 256 * 1024 ))
    TCP_RMIN=4096; TCP_RDEF=$(( 87 * 1024 )); TCP_RMAX=$(( 8 * 1024 * 1024 ))
    TCP_WMIN=4096; TCP_WDEF=$(( 65 * 1024 )); TCP_WMAX=$(( 8 * 1024 * 1024 ))
    UDP_RMIN=4096; UDP_RDEF=$(( 512 * 1024 )); UDP_RMAX=$(( 8 * 1024 * 1024 ))
    BACKLOG=1024; SOMAXCONN=2048; TW_BUCKETS=131072; NETDEV_BACKLOG=3000
    MAX_ORPHANS=32768

elif [[ $RAM_MB -lt 8192 ]]; then
    TIER="large (2–8GB)"
    CORE_RMAX=$(( 16 * 1024 * 1024 ))
    CORE_WMAX=$(( 16 * 1024 * 1024 ))
    CORE_RDEF=$(( 512 * 1024 ))
    CORE_WDEF=$(( 512 * 1024 ))
    TCP_RMIN=4096; TCP_RDEF=$(( 87 * 1024 )); TCP_RMAX=$(( 16 * 1024 * 1024 ))
    TCP_WMIN=4096; TCP_WDEF=$(( 65 * 1024 )); TCP_WMAX=$(( 16 * 1024 * 1024 ))
    UDP_RMIN=4096; UDP_RDEF=$(( 1024 * 1024 )); UDP_RMAX=$(( 16 * 1024 * 1024 ))
    BACKLOG=4096; SOMAXCONN=8192; TW_BUCKETS=262144; NETDEV_BACKLOG=5000
    MAX_ORPHANS=65536

else
    TIER="xlarge (>8GB)"
    CORE_RMAX=$(( 32 * 1024 * 1024 ))
    CORE_WMAX=$(( 32 * 1024 * 1024 ))
    CORE_RDEF=$(( 1024 * 1024 ))
    CORE_WDEF=$(( 1024 * 1024 ))
    TCP_RMIN=4096; TCP_RDEF=$(( 87 * 1024 )); TCP_RMAX=$(( 32 * 1024 * 1024 ))
    TCP_WMIN=4096; TCP_WDEF=$(( 65 * 1024 )); TCP_WMAX=$(( 32 * 1024 * 1024 ))
    UDP_RMIN=4096; UDP_RDEF=$(( 2 * 1024 * 1024 )); UDP_RMAX=$(( 32 * 1024 * 1024 ))
    # 65535 is the kernel somaxconn limit before 5.4; larger values are unnecessary.
    BACKLOG=8192; SOMAXCONN=65535; TW_BUCKETS=1440000; NETDEV_BACKLOG=5000
    MAX_ORPHANS=131072
fi

# Calculate tcp_mem from the actual RAM size, in pages.
TCP_MEM_MAX=$(( RAM_PAGES / 4 ))          # 25% RAM
TCP_MEM_PRESSURE=$(( TCP_MEM_MAX * 3 / 4 ))
TCP_MEM_MIN=$(( TCP_MEM_PRESSURE / 2 ))

info "Tier: ${BOLD}${TIER}${NC}"
info "tcp_mem: ${TCP_MEM_MIN} ${TCP_MEM_PRESSURE} ${TCP_MEM_MAX} pages \
(max ≈ $(( TCP_MEM_MAX * 4 / 1024 )) MB)"

# ── Kernel version ─────────────────────────────────────────────────────────
KERNEL=$(uname -r)
info "Kernel: ${KERNEL}"

# ── BBR ─────────────────────────────────────────────────────────────────────
BBR_OK=0
if modprobe tcp_bbr 2>/dev/null; then
    BBR_OK=1
    ok "tcp_bbr module loaded"
else
    warn "tcp_bbr is unavailable; keeping cubic"
fi

# ── Helpers ────────────────────────────────────────────────────────────────
apply() {
    local key=$1 val=$2
    if sysctl -qw "${key}=${val}" 2>/dev/null; then
        ok "${key} = ${val}"
    else
        warn "Could not apply: ${key} (unavailable in this kernel?)"
    fi
}

# H-4 fix: "$key" quoted inside subshell
apply_if_exists() {
    local key=$1 val=$2
    if [[ -f "/proc/sys/$(echo "$key" | tr . /)" ]]; then
        apply "$key" "$val"
    else
        skip "${key} — unavailable in kernel ${KERNEL}"
    fi
}

# ── Apply sysctl parameters ────────────────────────────────────────────────
echo -e "\n${BOLD}── Global buffers ─────────────────────────────────────${NC}"
apply net.core.rmem_max         "$CORE_RMAX"
apply net.core.wmem_max         "$CORE_WMAX"
apply net.core.rmem_default     "$CORE_RDEF"
apply net.core.wmem_default     "$CORE_WDEF"

echo -e "\n${BOLD}── Per-socket TCP buffers ──────────────────────────────${NC}"
apply net.ipv4.tcp_rmem         "${TCP_RMIN} ${TCP_RDEF} ${TCP_RMAX}"
apply net.ipv4.tcp_wmem         "${TCP_WMIN} ${TCP_WDEF} ${TCP_WMAX}"
apply net.ipv4.tcp_mem          "${TCP_MEM_MIN} ${TCP_MEM_PRESSURE} ${TCP_MEM_MAX}"

echo -e "\n${BOLD}── UDP buffers (WireGuard) ─────────────────────────────${NC}"
# WireGuard uses UDP; TCP buffers do not affect it.
apply net.ipv4.udp_rmem_min     "$UDP_RMIN"
apply net.ipv4.udp_wmem_min     "$UDP_RMIN"
apply net.core.optmem_max       "$UDP_RDEF"

echo -e "\n${BOLD}── Congestion control ───────────────────────────────${NC}"
apply net.core.default_qdisc fq
if [[ $BBR_OK -eq 1 ]]; then
    apply net.ipv4.tcp_congestion_control bbr
else
    apply net.ipv4.tcp_congestion_control cubic
fi

echo -e "\n${BOLD}── Queues ─────────────────────────────────────────────${NC}"
apply net.core.netdev_max_backlog  "$NETDEV_BACKLOG"
apply net.core.somaxconn           "$SOMAXCONN"
apply net.ipv4.tcp_max_syn_backlog "$BACKLOG"

echo -e "\n${BOLD}── TIME_WAIT ─────────────────────────────────────────${NC}"
apply net.ipv4.tcp_max_tw_buckets "$TW_BUCKETS"
# H-1 fix: tcp_tw_reuse=0 — value 1 can break NAT/MASQUERADE on a router:
# a reused port may receive RST from a remote host that still retains
# state for an older connection with the same 4-tuple.
apply net.ipv4.tcp_tw_reuse       0
apply net.ipv4.tcp_fin_timeout    15
# tcp_tw_recycle was removed in 4.12; always skip it on Ubuntu 22.04 (5.15+).
skip "net.ipv4.tcp_tw_recycle — removed in kernel 4.12+ (target: Ubuntu 22.04+)"

echo -e "\n${BOLD}── TCP features ───────────────────────────────────────${NC}"
# H-2 fix: tcp_fastopen=1 (client only), instead of 3 (client and server).
# fastopen=3 on a router with ip_forward=1 permits amplification:
# a spoofed SYN makes the router send SYN-ACK and data to another address.
apply net.ipv4.tcp_fastopen           1
apply net.ipv4.tcp_mtu_probing        1
apply net.ipv4.tcp_slow_start_after_idle 0
apply net.ipv4.tcp_sack               1
apply net.ipv4.tcp_dsack              1
apply net.ipv4.tcp_timestamps         1
apply net.ipv4.tcp_window_scaling     1
# M-4 fix: tcp_no_metrics_save was removed.
# Value 1 prevents caching RTT/SSTHRESH/PMTU between connections.
# This is counterproductive with BBR: BBR manages its own window,
# but the PMTU cache is needed to choose the correct MSS.
# H-3 fix: tcp_ecn=2 instead of 1.
# Value 1 initiates ECN negotiation; some providers and firewalls
# drop SYN packets with ECN bits, causing silent failures. Value 2 accepts
# ECN when offered by the peer without initiating negotiation.
apply net.ipv4.tcp_ecn                2
# M-3 fix: 300 seconds instead of 60.
# 60 seconds is too aggressive system-wide: every idle TCP connection,
# including management and SSH, sends keepalives after one minute.
# WireGuard manages its own UDP keepalives.
apply net.ipv4.tcp_keepalive_time     300
apply net.ipv4.tcp_keepalive_intvl    10
apply net.ipv4.tcp_keepalive_probes   6
# tcp_low_latency and tcp_fack were removed in 4.14/4.15; always skip them on Ubuntu 22.04.
skip "net.ipv4.tcp_low_latency — removed in kernel 4.14+"
skip "net.ipv4.tcp_fack — removed in kernel 4.15+ (superseded by SACK)"

echo -e "\n${BOLD}── Protection (SYN floods, orphans) ────────────────────${NC}"
apply net.ipv4.tcp_syncookies    1
apply net.ipv4.tcp_max_orphans   "$MAX_ORPHANS"
apply net.ipv4.tcp_syn_retries   3
apply net.ipv4.tcp_synack_retries 3

echo -e "\n${BOLD}── Forwarding and spoofing protection (VPN) ───────────${NC}"
apply net.ipv4.ip_forward 1
apply_if_exists net.ipv6.conf.all.forwarding 1
# L-5: rp_filter provides source-address validation on the forwarding path.
# Use mode 2 (loose): drop packets only when the source IP has no route
# through any interface. Mode 1 (strict) requires a symmetric source route
# and can break WireGuard and other VPNs:
# tunnel packets arrive on a WireGuard interface while their source route
# uses eth0; strict validation treats this as spoofing and drops them.
# conf.default applies to newly created interfaces, including wg0 and wg10.
apply net.ipv4.conf.all.rp_filter     2
apply net.ipv4.conf.default.rp_filter 2
# L-5: disable ICMP redirects on the router to prevent route poisoning
# through forged ICMP redirect messages.
apply net.ipv4.conf.all.accept_redirects   0
apply net.ipv4.conf.all.send_redirects     0
apply net.ipv4.conf.all.accept_source_route 0

echo -e "\n${BOLD}── Virtual memory (less swapping) ──────────────────────${NC}"
apply vm.swappiness          10
apply vm.vfs_cache_pressure  50

# ── C-2 fix: save the file BEFORE the tc loop ───────────────────────────────
# Even if tc fails, the sysctl.d file has already been saved
# and its settings will be applied after reboot.
SYSCTL_FILE="/etc/sysctl.d/99-cascade-tuning.conf"
echo -e "\n${BOLD}── Saving to ${SYSCTL_FILE} ────────────────────────────${NC}"

# L-3: back up the existing configuration before overwriting it.
if [[ -f "$SYSCTL_FILE" ]]; then
    cp "$SYSCTL_FILE" "${SYSCTL_FILE}.bak.$(date +%s)"
    info "Previous configuration backed up"
fi

cat > "$SYSCTL_FILE" << EOF
# Auto-generated by tcp_tune.sh
# RAM: ${RAM_MB} MB | Tier: ${TIER} | Kernel: ${KERNEL}
# $(date)

# ── Global buffers ─────────────────────────────────────────────────────────
net.core.rmem_max             = $CORE_RMAX
net.core.wmem_max             = $CORE_WMAX
net.core.rmem_default         = $CORE_RDEF
net.core.wmem_default         = $CORE_WDEF

# ── TCP buffers ────────────────────────────────────────────────────────────
net.ipv4.tcp_rmem             = $TCP_RMIN $TCP_RDEF $TCP_RMAX
net.ipv4.tcp_wmem             = $TCP_WMIN $TCP_WDEF $TCP_WMAX
net.ipv4.tcp_mem              = $TCP_MEM_MIN $TCP_MEM_PRESSURE $TCP_MEM_MAX

# ── UDP buffers (WireGuard) ─────────────────────────────────────────────────
net.ipv4.udp_rmem_min         = $UDP_RMIN
net.ipv4.udp_wmem_min         = $UDP_RMIN
net.core.optmem_max           = $UDP_RDEF

# ── Congestion control ────────────────────────────────────────────────────
net.core.default_qdisc        = fq
net.ipv4.tcp_congestion_control = $( [[ $BBR_OK -eq 1 ]] && echo bbr || echo cubic )

# ── Queues ─────────────────────────────────────────────────────────────────
net.core.netdev_max_backlog   = $NETDEV_BACKLOG
net.core.somaxconn            = $SOMAXCONN
net.ipv4.tcp_max_syn_backlog  = $BACKLOG

# ── TIME_WAIT ─────────────────────────────────────────────────────────────
net.ipv4.tcp_max_tw_buckets   = $TW_BUCKETS
# tw_reuse=0: value 1 can break NAT/MASQUERADE on a router.
net.ipv4.tcp_tw_reuse         = 0
net.ipv4.tcp_fin_timeout      = 15

# ── TCP features ───────────────────────────────────────────────────────────
# fastopen=1: client only; value 3 permits amplification on a forwarding router.
net.ipv4.tcp_fastopen         = 1
net.ipv4.tcp_mtu_probing      = 1
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_sack             = 1
net.ipv4.tcp_dsack            = 1
net.ipv4.tcp_timestamps       = 1
net.ipv4.tcp_window_scaling   = 1
# ecn=2: accept ECN when offered by the peer, without initiating it.
# Value 1 initiates ECN; some providers drop SYN packets with ECN bits.
net.ipv4.tcp_ecn              = 2
# keepalive=300s: 60s is too aggressive for a router-wide setting.
net.ipv4.tcp_keepalive_time   = 300
net.ipv4.tcp_keepalive_intvl  = 10
net.ipv4.tcp_keepalive_probes = 6

# ── Protection ─────────────────────────────────────────────────────────────
net.ipv4.tcp_syncookies       = 1
net.ipv4.tcp_max_orphans      = $MAX_ORPHANS
net.ipv4.tcp_syn_retries      = 3
net.ipv4.tcp_synack_retries   = 3

# ── VPN forwarding and spoofing protection ─────────────────────────────────
net.ipv4.ip_forward                 = 1
net.ipv6.conf.all.forwarding        = 1
# rp_filter=2 (loose): drop packets only when the source has no route at all.
# rp_filter=1 (strict) can break VPNs when a tunnel packet source is routed
# through another interface and strict validation treats it as spoofing.
net.ipv4.conf.all.rp_filter         = 2
net.ipv4.conf.default.rp_filter     = 2
net.ipv4.conf.all.accept_redirects  = 0
net.ipv4.conf.all.send_redirects    = 0
net.ipv4.conf.all.accept_source_route = 0

# ── VM ────────────────────────────────────────────────────────────────────
vm.swappiness                 = 10
vm.vfs_cache_pressure         = 50

# NOTE: the following parameters were removed from the kernel and are NOT applied:
# net.ipv4.tcp_low_latency  (removed in 4.14)
# net.ipv4.tcp_fack         (removed in 4.15, superseded by SACK)
# net.ipv4.tcp_tw_recycle   (removed in 4.12)
# net.ipv4.tcp_no_metrics_save is not enabled: counterproductive with BBR.
EOF

ok "Saved → ${SYSCTL_FILE}"

# ── fq on physical interfaces ──────────────────────────────────────────────
# C-2 fix: run this block AFTER saving the file so tc failures do not affect persistence.
# M-1 + L-4 fix: use a glob instead of ls; skip WireGuard/GRE/tunnel interfaces.
#
# Why wg*/awg* are skipped here but still receive fq:
# net.core.default_qdisc=fq above applies to NEW interfaces.
# WireGuard interfaces are created through the UI after this setting
# is applied, so they automatically receive fq when created.
# An explicit tc replace is redundant for them. Physical interfaces
# such as eth0/eth1 already exist at boot and require an explicit replace.
#
# fq on WireGuard interfaces, before transit tunnel encryption, provides
# fairness between client flows in a cascade topology:
# Client→wg10→wg11(fq)→encrypt→eth0. This happens automatically.
echo -e "\n${BOLD}── fq qdisc on physical interfaces ────────────────────${NC}"
for iface_path in /sys/class/net/*/; do
    iface="${iface_path%/}"
    iface="${iface##*/}"
    # Skip loopback.
    [[ "$iface" == "lo" ]] && continue
    # wg*/awg*: default_qdisc applies fq automatically when the interface
    # is created; an explicit replace is unnecessary (see above).
    case "$iface" in
        wg*|awg*|tun*|tap*|gre*|sit*|ip6tnl*|dummy*) continue ;;
        # Docker/veth: the daemon resets qdisc whenever a container restarts.
        veth*|br*|docker*) continue ;;
    esac
    if tc qdisc replace dev "$iface" root fq 2>/dev/null; then
        ok "fq → $iface"
    else
        warn "Could not apply fq to $iface (qdisc support may be unavailable)"
    fi
done

# ── Summary ────────────────────────────────────────────────────────────────
echo -e "\n${GREEN}${BOLD}══ Done ══════════════════════════════════════════${NC}"
printf "  %-16s %s\n" "RAM:"     "${RAM_MB} MB → tier: ${TIER}"
printf "  %-16s %s\n" "rmem:"    "$(( CORE_RMAX / 1024 / 1024 )) MB max"
printf "  %-16s %s\n" "wmem:"    "$(( CORE_WMAX / 1024 / 1024 )) MB max"
printf "  %-16s %s\n" "udp_buf:" "$(( UDP_RMAX / 1024 / 1024 )) MB max"
printf "  %-16s %s\n" "tcp_mem:" "$(( TCP_MEM_MAX * 4 / 1024 )) MB max (${TCP_MEM_MAX} pages)"
printf "  %-16s %s\n" "BBR:"     "$( [[ $BBR_OK -eq 1 ]] && echo enabled || echo 'cubic fallback' )"
printf "  %-16s %s\n" "Config:"  "${SYSCTL_FILE}"
