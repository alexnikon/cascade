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

func TestCoarsenPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		entries     []string
		granularity int
		want        []string
	}{
		{"disabled returns input unchanged", []string{"10.1.2.0/24"}, 0, []string{"10.1.2.0/24"}},
		{"host rounds up to /16", []string{"10.1.2.3/32"}, 16, []string{"10.1.0.0/16"}},
		{"missing mask treated as host", []string{"10.1.2.3"}, 16, []string{"10.1.0.0/16"}},
		{"already coarser is kept", []string{"10.0.0.0/8"}, 16, []string{"10.0.0.0/8"}},
		{"already exact is kept", []string{"10.1.0.0/16"}, 16, []string{"10.1.0.0/16"}},
		{"overlapping inputs collapse", []string{"10.1.2.0/24", "10.1.3.0/24"}, 16, []string{"10.1.0.0/16"}},
		{"host bits are masked", []string{"10.1.2.3/24"}, 16, []string{"10.1.0.0/16"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoarsenPrefixes(tc.entries, tc.granularity)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ", ") != strings.Join(tc.want, ", ") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	// A prefix already coarser than the target covers the rounded blocks by
	// itself, so it is kept whole — one entry instead of sixteen.
	if got, err := CoarsenPrefixes([]string{"10.0.0.0/12"}, 16); err != nil || len(got) != 1 || got[0] != "10.0.0.0/12" {
		t.Fatalf("10.0.0.0/12 -> %v, %v; want it kept as-is", got, err)
	}

	for _, bad := range []int{-1, 33} {
		if _, err := CoarsenPrefixes([]string{"10.0.0.0/8"}, bad); err == nil {
			t.Fatalf("accepted granularity /%d", bad)
		}
	}
	for _, bad := range [][]string{{"::/0"}, {"1.2.3.4/33"}, {"nonsense"}} {
		if _, err := CoarsenPrefixes(bad, 16); err == nil {
			t.Fatalf("accepted entries %v", bad)
		}
	}
}

// Rounding must never drop coverage: every address the input excluded is still
// excluded, and no emitted prefix may be finer than the requested granularity.
func TestCoarsenPrefixesCoversInput(t *testing.T) {
	entries := []string{"5.8.0.0/13", "31.13.144.0/21", "77.88.55.66/32", "93.186.224.0/19", "217.20.147.0/24"}
	got, err := CoarsenPrefixes(entries, 16)
	if err != nil {
		t.Fatal(err)
	}
	coarse := make([]netip.Prefix, 0, len(got))
	for _, e := range got {
		p := netip.MustParsePrefix(e)
		if p.Bits() > 16 {
			t.Fatalf("prefix %s is finer than /16", p)
		}
		coarse = append(coarse, p)
	}
	for _, e := range entries {
		p := netip.MustParsePrefix(e)
		for _, probe := range []netip.Addr{p.Addr(), lastAddr(p)} {
			covered := false
			for _, c := range coarse {
				covered = covered || c.Contains(probe)
			}
			if !covered {
				t.Fatalf("%s (from %s) not covered by %v", probe, p, got)
			}
		}
	}
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	n := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	n += uint32(1)<<(32-p.Bits()) - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
