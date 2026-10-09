package aliases

import (
	"errors"
	"fmt"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/ipset"
)

var ErrClientRouting = errors.New("client routing unavailable")

// ResolveClientAllowedIPs resolves the current snapshot of a referenced IPv4 ipset.
func ResolveClientAllowedIPs(id string) (string, error) {
	if instance == nil {
		return "", fmt.Errorf("%w: alias manager is unavailable", ErrClientRouting)
	}
	routes, err := instance.ClientAllowedIPs(id)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrClientRouting, err)
	}
	return routes, nil
}

func (m *Manager) ClientAllowedIPs(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("select an ipset alias")
	}
	a, err := m.getOrNotFound(id)
	if err != nil {
		return "", err
	}
	granularity := 0
	if a.GeneratorOpts != nil {
		granularity = a.GeneratorOpts.ClientGranularity
	}
	return m.clientAllowedIPs(a, granularity)
}

// ClientAllowedIPsAt is ClientAllowedIPs with an explicit rounding override, so
// the UI can preview what a different precision would cost before committing.
func (m *Manager) ClientAllowedIPsAt(id string, granularity int) (string, error) {
	if id == "" {
		return "", fmt.Errorf("select an ipset alias")
	}
	a, err := m.getOrNotFound(id)
	if err != nil {
		return "", err
	}
	return m.clientAllowedIPs(a, granularity)
}

func (m *Manager) clientAllowedIPs(a *Alias, granularity int) (string, error) {
	if a.Type != "ipset" {
		return "", fmt.Errorf("alias %q must be of type ipset", a.Name)
	}
	entries, err := m.ipsetMgr.ReadEntries(a.IPSetName)
	if err != nil {
		return "", fmt.Errorf("alias %q: %w", a.Name, err)
	}
	// A generated country/ASN alias is far too granular to use as a client
	// exclusion verbatim; round it outward when the alias asks for it.
	if granularity > 0 {
		entries, err = ipset.CoarsenPrefixes(entries, granularity)
		if err != nil {
			return "", fmt.Errorf("alias %q: %w", a.Name, err)
		}
	}
	routes, err := ipset.ExcludedClientRoutes(entries)
	if err != nil {
		return "", fmt.Errorf("alias %q: %w", a.Name, err)
	}
	return routes, nil
}

// checkClientRoutingReferences protects persistent client routing bindings.
func checkClientRoutingReferences(id string) error {
	var count int
	err := db.DB().QueryRow(`SELECT COUNT(*) FROM peers WHERE client_allowed_ips_mode = 'exclude-ipset' AND client_allowed_ips_alias_id = ?`, id).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("alias is used by %d client peer(s); change their routing mode first", count)
	}
	err = db.DB().QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'defaultClientAllowedIPsAliasId' AND value = ? AND EXISTS (SELECT 1 FROM settings WHERE key = 'defaultClientAllowedIPsMode' AND value = 'exclude-ipset')`, id).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("alias is used by Global Settings; change the default client routing first")
	}
	return nil
}
