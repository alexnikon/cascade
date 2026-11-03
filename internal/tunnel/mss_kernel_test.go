package tunnel

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Run only in an isolated network namespace with real iptables-nft.
func TestMSSKernelLifecycle(t *testing.T) {
	if os.Getenv("CASCADE_MSS_KERNEL_TEST") != "1" {
		t.Skip("requires isolated Linux network namespace")
	}
	run := func(command string) string {
		t.Helper()
		out, err := exec.Command("sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v: %s", err, out)
		}
		return string(out)
	}
	iface := newTestIface()
	iface.ID = "mss-test"
	iface.NatDisabled = true
	iface.MSS = -1
	hooks := func() (string, string) {
		cfg := iface.generateWgConfig()
		var up, down string
		for _, line := range strings.Split(cfg, "\n") {
			if strings.HasPrefix(line, "PostUp = ") {
				up = strings.TrimPrefix(line, "PostUp = ")
			}
			if strings.HasPrefix(line, "PostDown = ") {
				down = strings.TrimPrefix(line, "PostDown = ")
			}
		}
		return up, down
	}
	run("ip link add mss-test type dummy; ip link set mss-test up; iptables-nft -t mangle -A FORWARD -p udp -j ACCEPT")
	defer run("ip link del mss-test")
	up, down := hooks()
	run(up)
	run(up)
	count := func() int { return strings.Count(run("iptables-nft -t mangle -S FORWARD"), "-j TCPMSS") }
	if count() != 2 {
		t.Fatal("repeated startup added duplicates")
	}
	run("iptables-nft -t mangle -A FORWARD -i mss-test -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu")
	run(down)
	if count() != 0 {
		t.Fatal("legacy duplicates survived teardown")
	}
	for _, value := range []int{1260, 1280, 0, -1} {
		iface.MSS = value
		up, down = hooks()
		run(up)
		run(up)
		want := 2
		if value == 0 {
			want = 0
		}
		if count() != want {
			t.Fatal("incorrect MSS rules after mode change")
		}
		run(down)
	}
	iface.DisableRoutes = true
	up, down = hooks()
	run(up)
	if count() != 0 {
		t.Fatal("S2S installed client MSS")
	}
	run(down)
	if !strings.Contains(run("iptables-nft -t mangle -S FORWARD"), "-p udp -j ACCEPT") {
		t.Fatal("unrelated rule changed")
	}
}
