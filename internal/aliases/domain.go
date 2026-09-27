package aliases

// Domain aliases.
//
// A domain alias holds DNS names instead of addresses. A background resolver
// (internal/dnsalias) keeps two kernel ipsets in sync with what those names
// currently resolve to — one per address family — and firewall rules match the
// v4 set exactly as they match any other ipset alias.
//
// The configured names in `entries` are the source of truth; the resolved
// addresses live only in the kernel, and the resolver's runtime state (last
// update, error, counts) lives only in memory. Nothing transient is persisted.

import (
	"fmt"
	"net"
	"strings"
)

// Kernel ipset names are limited to 31 characters, so the alias's 36-character
// UUID cannot be used whole. We take the first domainIDLen hex digits of it
// (dashes stripped), which leaves 64 bits of entropy — collisions are checked
// for at create time anyway.
//
//	csc_dom_4ad4144236224d0e_v4   → 8 + 16 + 3 = 27 chars
const (
	domainSetPrefix = "csc_dom_"
	domainIDLen     = 16
	domainSuffixV4  = "_v4"
	domainSuffixV6  = "_v6"
)

// DomainStatus is the resolver's runtime view of a domain alias. It is attached
// to the API representation on read and is never stored in SQLite.
type DomainStatus struct {
	LastUpdate string `json:"lastUpdate,omitempty"` // RFC3339, last successful resolution
	NextUpdate string `json:"nextUpdate,omitempty"` // RFC3339, when the next attempt is due
	IPv4Count  int    `json:"ipv4Count"`
	IPv6Count  int    `json:"ipv6Count"`
	LastError  string `json:"lastError,omitempty"` // most recent DNS failure, cleared on success
	Resolving  bool   `json:"resolving,omitempty"`
}

// DomainSetV4 returns the deterministic kernel ipset name holding the IPv4
// addresses of the domain alias with this ID.
func DomainSetV4(aliasID string) string { return domainSetName(aliasID, domainSuffixV4) }

// DomainSetV6 returns the deterministic kernel ipset name holding the IPv6
// addresses of the domain alias with this ID.
func DomainSetV6(aliasID string) string { return domainSetName(aliasID, domainSuffixV6) }

// IsDomainSet reports whether a kernel set name was generated for a domain alias.
func IsDomainSet(setName string) bool { return strings.HasPrefix(setName, domainSetPrefix) }

func domainSetName(aliasID, suffix string) string {
	id := strings.ReplaceAll(strings.ToLower(aliasID), "-", "")
	if len(id) > domainIDLen {
		id = id[:domainIDLen]
	}
	return domainSetPrefix + id + suffix
}

// normalizeDomainEntries validates, lowercases and deduplicates the domain names
// of a domain alias, preserving the order in which they were given.
//
// Wildcards are rejected outright rather than silently ignored: "*.example.com"
// is not a resolvable name, and accepting it would imply a matching behaviour
// this stage does not provide.
func normalizeDomainEntries(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))

	for _, e := range entries {
		name := strings.ToLower(strings.TrimSpace(e))
		name = strings.TrimSuffix(name, ".") // accept a fully-qualified trailing dot
		if name == "" {
			continue
		}
		if err := validateDomainName(name); err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("domain alias requires at least one domain name")
	}
	return out, nil
}

// validateDomainName checks a single name against the RFC 1035 preferred syntax
// (as widened by RFC 1123 to allow a leading digit). Internationalised names are
// accepted in punycode form only.
func validateDomainName(name string) error {
	if strings.HasPrefix(name, "*") || strings.Contains(name, "*") {
		return fmt.Errorf("wildcard domains are not supported (got %q) — list each name explicitly", name)
	}
	if net.ParseIP(name) != nil {
		return fmt.Errorf("%q is an IP address — use a host or network alias instead", name)
	}
	if len(name) > 253 {
		return fmt.Errorf("domain name %q is longer than 253 characters", name)
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return fmt.Errorf("domain name %q must contain at least one dot", name)
	}
	for _, l := range labels {
		if err := validateDomainLabel(l, name); err != nil {
			return err
		}
	}
	return nil
}

func validateDomainLabel(label, name string) error {
	if label == "" {
		return fmt.Errorf("domain name %q contains an empty label", name)
	}
	if len(label) > 63 {
		return fmt.Errorf("domain name %q contains a label longer than 63 characters", name)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("domain name %q contains a label starting or ending with a hyphen", name)
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		isLetter := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !isDigit && c != '-' {
			return fmt.Errorf("domain name %q contains an invalid character %q — "+
				"use letters, digits and hyphens (punycode for non-ASCII names)", name, string(c))
		}
	}
	return nil
}

// domainSetPrefixInUse reports whether another alias already owns the truncated
// ID prefix that would be used for this one. Practically unreachable with 64
// bits of entropy, but a silent collision would cross-contaminate two firewall
// rules, so it is worth the one query.
func (m *Manager) domainSetPrefixInUse(setName string) (bool, error) {
	all, err := m.GetAll()
	if err != nil {
		return false, err
	}
	for _, a := range all {
		if a.Type == "domain" && a.IPSetName == setName {
			return true, nil
		}
	}
	return false, nil
}
