package dnsalias

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/alexnikon/cascade/internal/settings"
)

var ErrInvalidUpstreams = errors.New("invalid domain alias DNS settings")

type UpstreamConfig struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
}

type UpstreamStatus struct {
	UpstreamConfig
	EffectiveServers []string `json:"effectiveServers"`
}

func (cfg UpstreamConfig) endpoints() ([]string, error) {
	switch cfg.Mode {
	case "system":
		if len(cfg.Servers) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: system mode requires an empty server list", ErrInvalidUpstreams)
	case "custom":
		if len(cfg.Servers) < 1 || len(cfg.Servers) > 3 {
			return nil, fmt.Errorf("%w: enter one to three IP addresses", ErrInvalidUpstreams)
		}
	default:
		return nil, fmt.Errorf("%w: mode must be system or custom", ErrInvalidUpstreams)
	}

	endpoints := make([]string, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		ip, err := netip.ParseAddr(strings.TrimSpace(server))
		if err != nil || ip.Zone() != "" {
			return nil, fmt.Errorf("%w: %q is not an IPv4 or IPv6 address", ErrInvalidUpstreams, server)
		}
		endpoint := net.JoinHostPort(ip.Unmap().String(), "53")
		if slices.Contains(endpoints, endpoint) {
			return nil, fmt.Errorf("%w: duplicate IP address %s", ErrInvalidUpstreams, ip.Unmap())
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}

func (r *Resolver) aliasDNS() *dnsClient {
	if client := r.customUpstreams.Load(); client != nil {
		return client
	}
	return r.dc
}

func (r *Resolver) Upstreams() UpstreamStatus {
	custom := r.customUpstreams.Load()
	if custom == nil {
		return UpstreamStatus{
			UpstreamConfig:   UpstreamConfig{Mode: "system", Servers: []string{}},
			EffectiveServers: slices.Clone(r.dc.servers),
		}
	}
	return UpstreamStatus{
		UpstreamConfig:   UpstreamConfig{Mode: "custom", Servers: endpointIPs(custom.servers)},
		EffectiveServers: slices.Clone(custom.servers),
	}
}

func endpointIPs(endpoints []string) []string {
	ips := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ip, _, _ := net.SplitHostPort(endpoint)
		ips = append(ips, ip)
	}
	return ips
}

// LoadUpstreams restores the alias DNS configuration before Start.
func (r *Resolver) LoadUpstreams(db *sql.DB) error {
	var stored string
	err := db.QueryRow("SELECT value FROM settings WHERE key = ?", settings.DomainAliasDNSKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && stored == "") {
		return nil
	}
	if err != nil {
		return err
	}
	endpoints, err := (UpstreamConfig{Mode: "custom", Servers: strings.Split(stored, ",")}).endpoints()
	if err != nil {
		return err
	}
	r.customUpstreams.Store(newDNSClient(endpoints))
	return nil
}

// UpdateUpstreams persists the configuration before making it visible to workers.
func (r *Resolver) UpdateUpstreams(db *sql.DB, cfg UpstreamConfig) (UpstreamStatus, error) {
	endpoints, err := cfg.endpoints()
	if err != nil {
		return UpstreamStatus{}, err
	}
	r.upstreamMu.Lock()
	defer r.upstreamMu.Unlock()
	_, err = db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		settings.DomainAliasDNSKey, strings.Join(endpointIPs(endpoints), ","))
	if err != nil {
		return UpstreamStatus{}, err
	}
	if len(endpoints) == 0 {
		r.customUpstreams.Store(nil)
	} else {
		r.customUpstreams.Store(newDNSClient(endpoints))
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, state := range r.states {
		select {
		case state.wake <- struct{}{}:
		default: // A refresh is already queued.
		}
	}
	return r.Upstreams(), nil
}
