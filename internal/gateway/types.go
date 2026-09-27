// Package gateway manages gateways, gateway groups, and their live monitoring.
// Mirrors Gateway.js, GatewayGroup.js, GatewayMonitor.js, and GatewayManager.js
// from the Node.js version.
//
// Storage: SQLite tables `gateways` and `gateway_groups`.
// Monitoring: per-gateway goroutine pairs (ICMP + HTTP) with sliding windows.
package gateway

import (
	"net"
	"strings"
)

// MonitorHttpConfig holds HTTP probe parameters for a gateway.
// Stored as JSON in the `monitor_http` column.
type MonitorHttpConfig struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expectedStatus"` // 0 → 200
	Interval       int    `json:"interval"`       // seconds, 0 → 10
	Timeout        int    `json:"timeout"`        // seconds, 0 → 5
}

// Gateway is a monitored next-hop or upstream endpoint.
// Mirrors the Gateway.js model.
type Gateway struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Interface        string            `json:"interface"`        // outbound host interface (eth0, wg10, …)
	GatewayIP        string            `json:"gatewayIP"`        // IPv4 next-hop for routing
	MonitorAddress   string            `json:"monitorAddress"`   // ICMP target; '' → use GatewayIP
	// IPv6 routing and health. Both optional — an IPv4-only gateway leaves them
	// empty and behaves exactly as before.
	//
	// GatewayIPv6 is an explicit IPv6 next hop. When it is empty the gateway can
	// still carry IPv6 if its interface holds a global IPv6 address, in which
	// case a device route is used (the normal shape for a WireGuard tunnel).
	//
	// MonitorAddressV6 is an ICMPv6 probe target. It is what makes IPv6 health
	// real rather than inferred: without it, an IPv4 probe is the only evidence
	// available and IPv6 health is inherited from it. See MonitorStatus.
	GatewayIPv6      string            `json:"gatewayIPv6"`
	MonitorAddressV6 string            `json:"monitorAddressV6"`
	Enabled          bool              `json:"enabled"`
	Monitor          bool              `json:"monitor"`
	MonitorInterval  int               `json:"monitorInterval"`  // ICMP probe interval, seconds
	WindowSeconds    int               `json:"windowSeconds"`    // sliding window; 0 → use global default
	LatencyThreshold int               `json:"latencyThreshold"` // ms, for display only
	MonitorHttp      MonitorHttpConfig `json:"monitorHttp"`
	MonitorRule      string            `json:"monitorRule"` // icmp_only | http_only | all | any
	Description      string            `json:"description"`
	AdminDown        bool              `json:"adminDown"`
	CreatedAt        string            `json:"createdAt"`
}

// GatewayGroupMember is one entry inside a GatewayGroup.
// Tier 1 = highest priority. Weight is used for load balancing within the same tier.
type GatewayGroupMember struct {
	GatewayID string `json:"gatewayId"`
	Tier      int    `json:"tier"`
	Weight    int    `json:"weight"`
}

// GatewayGroup aggregates gateways with tier-based failover.
// Mirrors GatewayGroup.js.
type GatewayGroup struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Trigger     string               `json:"trigger"` // packetloss | latency | packetloss_latency
	Description string               `json:"description"`
	Gateways    []GatewayGroupMember `json:"gateways"` // stored as JSON in DB column `members`
	CreatedAt   string               `json:"createdAt"`
}

// MonitorStatus is the live probe state of a gateway returned by Monitor.GetStatus.
type MonitorStatus struct {
	Status        string  `json:"status"`        // unknown | healthy | degraded | down | admin_down
	Latency       *int    `json:"latency"`       // ICMP avg latency ms; nil if no data
	PacketLoss    *int    `json:"packetLoss"`    // ICMP loss %; nil if no data
	LastCheck     *string `json:"lastCheck"`     // ISO8601
	HttpStatus    *string `json:"httpStatus"`    // nil if HTTP not configured
	HttpLatency   *int    `json:"httpLatency"`   // nil if no HTTP data
	HttpLastCheck *string `json:"httpLastCheck"` // nil if no HTTP probe yet
	HttpCode      *int    `json:"httpCode"`      // last HTTP response code; nil if no probe yet

	// Per-family health.
	//
	// IPv6Status is what IPv6 policy routing consults. IPv6Source says where it
	// came from, which is the honest part of the model:
	//
	//	"probe"     — a dedicated ICMPv6 probe against MonitorAddressV6
	//	"inherited" — no IPv6 probe target is configured, so the IPv4 result is
	//	              the only evidence there is. Reported as such rather than
	//	              presented as proof of IPv6 reachability.
	IPv6Status     string `json:"ipv6Status,omitempty"`
	IPv6Source     string `json:"ipv6Source,omitempty"`
	IPv6Latency    *int   `json:"ipv6Latency"`
	IPv6PacketLoss *int   `json:"ipv6PacketLoss"`
}

// GatewayWithStatus combines Gateway data with its live monitoring status.
// This is what the API returns.
// RealStatus holds the actual probe status when AdminDown=true (so the UI can
// show the underlying health while the gateway is administratively disabled).
type GatewayWithStatus struct {
	Gateway
	MonitorStatus
	RealStatus string `json:"realStatus,omitempty"`
}

// isHostname reports whether s is a name rather than a literal IP address.
func isHostname(s string) bool { return net.ParseIP(s) == nil }

// isIPv6 reports whether s parses as an IPv6 address (and not an IPv4 one).
// net.ParseIP accepts both and normalises IPv4 into a 16-byte form, so the
// presence of a colon is what actually distinguishes the two.
func isIPv6(s string) bool {
	return net.ParseIP(s) != nil && strings.Contains(s, ":")
}
