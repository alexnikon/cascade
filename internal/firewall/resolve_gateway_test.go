package firewall

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/gateway"
)

func addPBRTestGateway(t *testing.T, manager *gateway.Manager, name, ip, iface string) *gateway.Gateway {
	t.Helper()
	gw, err := manager.CreateGateway(gateway.GatewayInput{
		Name:            name,
		Interface:       iface,
		GatewayIP:       ip,
		MonitorAddress:  ip,
		Enabled:         boolPtr(true),
		Monitor:         boolPtr(true),
		MonitorInterval: 60,
		WindowSeconds:   30,
		MonitorRule:     "icmp_only",
	})
	if err != nil {
		t.Fatalf("CreateGateway(%s): %v", name, err)
	}
	t.Cleanup(func() { manager.Monitor().Stop(gw.ID) })
	return gw
}

func boolPtr(value bool) *bool { return &value }

func TestPBRGatewayGroupStateMachine(t *testing.T) {
	m, _ := initTestDB(t)
	g1 := addPBRTestGateway(t, m.gm, "primary", "198.51.100.1", "eth1")
	g2 := addPBRTestGateway(t, m.gm, "secondary", "198.51.100.2", "eth2")
	group, err := m.gm.CreateGroup(gateway.GatewayGroupInput{Name: "failover", Gateways: []gateway.GatewayGroupMember{
		{GatewayID: g1.ID, Tier: 1}, {GatewayID: g2.ID, Tier: 2},
	}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	rule, err := m.AddRule(RuleInput{
		Name:           "group PBR",
		Action:         "accept",
		GatewayGroupID: group.ID,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	var commandsMu sync.Mutex
	var commands []string
	oldExec := pbrRouteExec
	oldRuleExec := pbrRuleExec
	pbrRouteExec = func(cmd string, timeout time.Duration, logCommand bool) (string, error) {
		commandsMu.Lock()
		commands = append(commands, cmd)
		commandsMu.Unlock()
		return "", nil
	}
	pbrRuleExec = func(cmd string, timeout time.Duration, logCommand bool) (string, error) {
		return "", nil
	}
	t.Cleanup(func() {
		pbrRouteExec = oldExec
		pbrRuleExec = oldRuleExec
	})

	commandCount := func() int {
		commandsMu.Lock()
		defer commandsMu.Unlock()
		return len(commands)
	}
	lastCommand := func() string {
		commandsMu.Lock()
		defer commandsMu.Unlock()
		if len(commands) == 0 {
			return ""
		}
		return commands[len(commands)-1]
	}
	waitForCommand := func(want string) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(lastCommand(), want) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("timed out waiting for route command containing %q; last command = %q", want, lastCommand())
	}
	apply := func() {
		if err := m.applyRoutingForRule(rule); err != nil {
			t.Fatalf("applyRoutingForRule: %v", err)
		}
	}

	// Tier 1 UP / Tier 2 UP selects the primary gateway.
	apply()
	if !strings.Contains(lastCommand(), "via 198.51.100.1 dev eth1") {
		t.Fatalf("initial PBR command = %q, want primary gateway", lastCommand())
	}

	// A non-selected member transition must not churn the route.
	before := commandCount()
	m.gm.Monitor().SetAdminDown(g2.ID, true)
	if err := m.onGatewayDown(g2.ID); err != nil {
		t.Fatalf("onGatewayDown(secondary): %v", err)
	}
	if commandCount() != before {
		t.Fatalf("route changed when selected gateway was unchanged")
	}
	m.gm.Monitor().SetAdminDown(g2.ID, false)

	// Tier 1 DOWN / Tier 2 UP fails over while the group remains overall UP.
	m.gm.Monitor().SetAdminDown(g1.ID, true)
	if err := m.onGatewayDown(g1.ID); err != nil {
		t.Fatalf("onGatewayDown(primary): %v", err)
	}
	if !strings.Contains(lastCommand(), "via 198.51.100.2 dev eth2") {
		t.Fatalf("failover command = %q, want secondary gateway", lastCommand())
	}
	if allDown, err := m.isGroupAllDown(group.ID); err != nil || allDown {
		t.Fatalf("group status after primary failure = allDown:%t err:%v, want overall UP", allDown, err)
	}

	// Partial recovery of the secondary restores the usable failover path.
	m.gm.Monitor().SetAdminDown(g2.ID, false)
	if err := m.onGatewayUp(g2.ID); err != nil {
		t.Fatalf("onGatewayUp(secondary): %v", err)
	}

	// Primary recovery must fail back automatically, without a manual save.
	m.restoreDelay = time.Millisecond
	m.gm.Monitor().SetAdminDown(g1.ID, false)
	if err := m.onGatewayUp(g1.ID); err != nil {
		t.Fatalf("onGatewayUp(primary): %v", err)
	}
	waitForCommand("via 198.51.100.1 dev eth1")

	// Total outage keeps the existing blackhole fallback behavior.
	m.gm.Monitor().SetAdminDown(g1.ID, true)
	m.gm.Monitor().SetAdminDown(g2.ID, true)
	if err := m.onGatewayDown(g1.ID); err != nil {
		t.Fatalf("onGatewayDown(primary, total outage): %v", err)
	}
	if err := m.onGatewayDown(g2.ID); err != nil {
		t.Fatalf("onGatewayDown(secondary, total outage): %v", err)
	}
	if !strings.Contains(lastCommand(), "ip route replace blackhole default") {
		t.Fatalf("total outage command = %q, want blackhole fallback", lastCommand())
	}

	// Secondary partial recovery restores it while primary remains down.
	m.gm.Monitor().SetAdminDown(g2.ID, false)
	if err := m.onGatewayUp(g2.ID); err != nil {
		t.Fatalf("onGatewayUp(secondary, partial recovery): %v", err)
	}
	waitForCommand("via 198.51.100.2 dev eth2")
}

func TestPBRGatewayGroupCallbacksSerializeSameRule(t *testing.T) {
	m, _ := initTestDB(t)
	g1 := addPBRTestGateway(t, m.gm, "primary", "198.51.100.1", "eth1")
	g2 := addPBRTestGateway(t, m.gm, "secondary", "198.51.100.2", "eth2")
	group, err := m.gm.CreateGroup(gateway.GatewayGroupInput{Name: "failover", Gateways: []gateway.GatewayGroupMember{
		{GatewayID: g1.ID, Tier: 1}, {GatewayID: g2.ID, Tier: 2},
	}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	rule, err := m.AddRule(RuleInput{Name: "serialized PBR", Action: "accept", GatewayGroupID: group.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	oldExec := pbrRouteExec
	oldRuleExec := pbrRuleExec
	var mu sync.Mutex
	commands := 0
	pbrRouteExec = func(cmd string, timeout time.Duration, logCommand bool) (string, error) {
		mu.Lock()
		commands++
		mu.Unlock()
		time.Sleep(time.Millisecond)
		return "", nil
	}
	pbrRuleExec = func(cmd string, timeout time.Duration, logCommand bool) (string, error) {
		return "", nil
	}
	t.Cleanup(func() {
		pbrRouteExec = oldExec
		pbrRuleExec = oldRuleExec
	})
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	m.gm.Monitor().SetAdminDown(g1.ID, true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.onGatewayDown(g1.ID); err != nil {
				t.Errorf("onGatewayDown: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	got := commands
	mu.Unlock()
	if got != 2 {
		t.Fatalf("route command count = %d, want initial apply plus one failover", got)
	}
}

func TestReplacePBRRoute_PreservesAWGDeviceRoute(t *testing.T) {
	m := New(nil, nil)
	fwmark := 1001
	rule := &Rule{ID: "awg-route", Fwmark: &fwmark}
	oldExec := pbrRouteExec
	var command string
	pbrRouteExec = func(cmd string, timeout time.Duration, logCommand bool) (string, error) {
		command = cmd
		return "", nil
	}
	t.Cleanup(func() { pbrRouteExec = oldExec })

	if err := m.replacePBRRoute(rule, resolvedGW{gatewayIP: "192.0.2.1", iface: "awg0"}); err != nil {
		t.Fatalf("replacePBRRoute: %v", err)
	}
	if strings.Contains(command, " via ") || !strings.Contains(command, "default dev awg0") {
		t.Fatalf("AWG route command = %q, want device-only route", command)
	}
}
