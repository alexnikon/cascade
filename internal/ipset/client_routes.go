package ipset

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/alexnikon/cascade/internal/util"
)

// ReadEntries reads a complete kernel snapshot and preserves command failures.
func (m *Manager) ReadEntries(name string) ([]string, error) {
	if err := m.validateName(name); err != nil {
		return nil, err
	}
	out, err := util.ExecDefault(fmt.Sprintf("ipset save %s", name))
	if err != nil {
		return nil, fmt.Errorf("read ipset %s: %w", name, err)
	}
	var entries []string
	found := false
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "create" && fields[1] == name {
			found = true
		}
		if len(fields) >= 3 && fields[0] == "add" && fields[1] == name {
			entries = append(entries, fields[2])
		}
	}
	if !found {
		return nil, fmt.Errorf("ipset %s is unavailable", name)
	}
	return entries, nil
}

type routeNode struct {
	excluded bool
	children [2]*routeNode
}

// CoarsenPrefixes rounds each IPv4 prefix outward to the given prefix length.
//
// A generated country alias can hold many thousands of scattered prefixes, and
// the complement of that set is larger still — far too many routes to push into
// a client configuration (a country-sized list yields tens of thousands of
// CIDRs). Rounding each prefix up to a coarser block trades a small amount of
// neighbouring address space for a dramatically smaller complement, which is
// what makes the "exclude this alias" client mode practical for a whole country.
//
// The returned set covers everything the input covered plus the rounding
// margin; granularity 0 returns the input unchanged.
func CoarsenPrefixes(entries []string, granularity int) ([]string, error) {
	if granularity == 0 {
		return entries, nil
	}
	if granularity < 0 || granularity > 32 {
		return nil, fmt.Errorf("invalid prefix granularity /%d", granularity)
	}

	seen := make(map[netip.Prefix]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	add := func(p netip.Prefix) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p.String())
	}

	step := uint32(1) << (32 - granularity)
	for _, entry := range entries {
		text := strings.TrimSpace(entry)
		if !strings.Contains(text, "/") {
			text += "/32"
		}
		prefix, err := netip.ParsePrefix(text)
		if err != nil || !prefix.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 ipset entry %q", entry)
		}
		prefix = prefix.Masked()
		if prefix.Bits() <= granularity {
			add(prefix)
			continue
		}
		addr := prefix.Addr().As4()
		number := uint32(addr[0])<<24 | uint32(addr[1])<<16 | uint32(addr[2])<<8 | uint32(addr[3])
		span := uint32(1) << (32 - prefix.Bits())
		base := number / step * step
		lastBase := (number + span - 1) / step * step
		for {
			add(netip.PrefixFrom(netip.AddrFrom4([4]byte{
				byte(base >> 24), byte(base >> 16), byte(base >> 8), byte(base),
			}), granularity))
			if base == lastBase {
				break
			}
			base += step
		}
	}
	return out, nil
}

// ExcludedClientRoutes returns the minimal IPv4 complement plus an IPv6 default route.
// A prefix trie bounds processing by 32 steps per input prefix without enumerating hosts.
func ExcludedClientRoutes(entries []string) (string, error) {
	if len(entries) == 0 {
		return "", fmt.Errorf("ipset has no IPv4 entries")
	}
	root := &routeNode{}
	for _, entry := range entries {
		text := strings.TrimSpace(entry)
		if !strings.Contains(text, "/") {
			text += "/32"
		}
		prefix, err := netip.ParsePrefix(text)
		if err != nil || !prefix.Addr().Is4() {
			return "", fmt.Errorf("invalid IPv4 ipset entry %q", entry)
		}
		prefix = prefix.Masked()
		addr := prefix.Addr().As4()
		number := uint32(addr[0])<<24 | uint32(addr[1])<<16 | uint32(addr[2])<<8 | uint32(addr[3])
		node := root
		for depth := 0; depth < prefix.Bits() && !node.excluded; depth++ {
			bit := (number >> (31 - depth)) & 1
			if node.children[bit] == nil {
				node.children[bit] = &routeNode{}
			}
			node = node.children[bit]
		}
		node.excluded = true
		node.children = [2]*routeNode{}
	}
	// Collapse sibling exclusions before emitting the complement.
	var collapse func(*routeNode) bool
	collapse = func(node *routeNode) bool {
		if node == nil {
			return false
		}
		if node.excluded {
			return true
		}
		left, right := collapse(node.children[0]), collapse(node.children[1])
		if left && right {
			node.excluded = true
			node.children = [2]*routeNode{}
		}
		return node.excluded
	}
	collapse(root)
	var routes []string
	var emit func(*routeNode, uint32, int)
	emit = func(node *routeNode, address uint32, depth int) {
		if node == nil {
			addr := netip.AddrFrom4([4]byte{byte(address >> 24), byte(address >> 16), byte(address >> 8), byte(address)})
			routes = append(routes, netip.PrefixFrom(addr, depth).String())
			return
		}
		if node.excluded {
			return
		}
		emit(node.children[0], address, depth+1)
		emit(node.children[1], address|(uint32(1)<<(31-depth)), depth+1)
	}
	emit(root, 0, 0)
	routes = append(routes, "::/0")
	return strings.Join(routes, ", "), nil
}
