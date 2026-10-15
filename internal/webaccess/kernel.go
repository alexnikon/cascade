package webaccess

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const chain = "CASCADE_WEB_ACCESS"

// RestoreScript updates only our source/interface guards. No ACCEPT verdict is
// emitted, so host firewall and fail2ban policies still evaluate every request.
func RestoreScript(rules []Rule, v6 bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*filter\n:%s - [0:0]\n-F %s\n", chain, chain)
	for _, r := range rules {
		if r.Source.Addr().Is6() != v6 {
			continue
		}
		for _, proto := range []string{"tcp", "udp"} {
			fmt.Fprintf(&b, "-A %s -s %s ! -i %s -p %s --dport 443 -j DROP\n", chain, r.Source, r.Interface, proto)
		}
	}
	b.WriteString("COMMIT\n")
	return b.String()
}

func command(input string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	if input != "" {
		c.Stdin = strings.NewReader(input)
	}
	var output bytes.Buffer
	c.Stdout = &output
	c.Stderr = &output
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

func ApplyKernel(rules []Rule) error {
	if runtime.GOOS != "linux" {
		if len(rules) == 0 {
			return nil
		}
		return fmt.Errorf("web access guards require Linux")
	}
	for _, family := range []struct {
		bin string
		v6  bool
	}{{"iptables-nft", false}, {"ip6tables-nft", true}} {
		// An unrestricted installation needs no new kernel capabilities or chains.
		if len(rules) == 0 {
			if err := command("", family.bin, "-w", "3", "-S", chain); err != nil {
				continue
			}
		}
		// Move our hook ahead of generic established/accept rules in the same
		// nft transaction as the chain update, without a temporary bypass window.
		hook := ""
		if command("", family.bin, "-w", "3", "-C", "INPUT", "-j", chain) == nil {
			hook = "-D INPUT -j " + chain + "\n"
		}
		hook += "-I INPUT 1 -j " + chain + "\nCOMMIT\n"
		script := strings.Replace(RestoreScript(rules, family.v6), "COMMIT\n", hook, 1)
		if err := command(script, family.bin+"-restore", "--wait", "3", "--noflush"); err != nil {
			return err
		}
	}
	return nil
}
