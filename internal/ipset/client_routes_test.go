package ipset

import (
	"math/rand"
	"net/netip"
	"strings"
	"testing"
)

func TestExcludedClientRoutes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		want    string
	}{
		{"lower half", []string{"0.0.0.0/1"}, "128.0.0.0/1, ::/0"},
		{"upper half", []string{"128.0.0.0/1"}, "0.0.0.0/1, ::/0"},
		{"whole space", []string{"0.0.0.0/0"}, "::/0"},
		{"siblings and overlaps", []string{"0.0.0.0/2", "64.0.0.0/2", "0.0.0.0/8", "64.0.0.0/2"}, "128.0.0.0/1, ::/0"},
		{"host bits", []string{"0.1.2.3/1"}, "128.0.0.0/1, ::/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExcludedClientRoutes(tc.entries)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, entries := range [][]string{nil, {"bad"}, {"::/0"}, {"1.2.3.4/33"}, {"0.0.0.0/1", "invalid"}} {
		if _, err := ExcludedClientRoutes(entries); err == nil {
			t.Fatalf("accepted %v", entries)
		}
	}
}

// Compare routing membership against the original exclusions, including every
// prefix boundary and deterministic random addresses across the IPv4 space.
func TestExcludedClientRoutesMembership(t *testing.T) {
	entries := []string{"0.0.0.0", "255.255.255.255", "10.0.0.0/8", "10.0.0.0/16", "192.0.2.1", "198.51.100.0/24", "203.0.113.0/25"}
	got, err := ExcludedClientRoutes(entries)
	if err != nil {
		t.Fatal(err)
	}
	var excluded, included []netip.Prefix
	var probes []netip.Addr
	for _, entry := range entries {
		if !strings.Contains(entry, "/") {
			entry += "/32"
		}
		p := netip.MustParsePrefix(entry)
		excluded = append(excluded, p)
		probes = append(probes, p.Addr(), p.Addr().Prev(), p.Addr().Next())
	}
	for _, entry := range strings.Split(got, ", ") {
		p := netip.MustParsePrefix(entry)
		if p.Addr().Is4() {
			included = append(included, p)
		}
	}
	rng := rand.New(rand.NewSource(17))
	for i := 0; i < 10000; i++ {
		n := rng.Uint32()
		probes = append(probes, netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}))
	}
	for _, address := range probes {
		if !address.IsValid() {
			continue
		}
		bypass, routes := false, 0
		for _, p := range excluded {
			bypass = bypass || p.Contains(address)
		}
		for _, p := range included {
			if p.Contains(address) {
				routes++
			}
		}
		if (bypass && routes != 0) || (!bypass && routes != 1) {
			t.Fatalf("address %s: excluded=%v route count=%d", address, bypass, routes)
		}
	}
}
