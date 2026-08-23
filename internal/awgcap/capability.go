// Package awgcap detects the maximum AmneziaWG protocol supported by runtime.
package awgcap

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	EngineVersion = "3.1.20260814"
	ToolsVersion  = "3.1.20260812"
)

// Capability describes the active AmneziaWG runtime.
type Capability struct {
	EngineVersion string
	ToolsVersion  string
	MaxProtocol   string
	AWG3Supported bool
	SupportError  string
}

// Detect returns a deterministic result for the pinned userspace image and
// verifies the installed kernel module version in kernel mode.
func Detect() Capability {
	if os.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION") == "amneziawg-go" {
		return Capability{EngineVersion: EngineVersion, ToolsVersion: ToolsVersion, MaxProtocol: "3.1", AWG3Supported: true}
	}
	version, err := kernelModuleVersion()
	if err != nil {
		return Capability{MaxProtocol: "2.0", SupportError: err.Error()}
	}
	major := parseMajor(version)
	if major < 3 {
		return Capability{EngineVersion: version, MaxProtocol: "2.0", SupportError: fmt.Sprintf("AmneziaWG kernel module %s does not support AWG 3.1", version)}
	}
	return Capability{EngineVersion: version, MaxProtocol: "3.1", AWG3Supported: true}
}

func kernelModuleVersion() (string, error) {
	if data, err := os.ReadFile("/sys/module/amneziawg/version"); err == nil {
		if version := strings.TrimSpace(string(data)); version != "" {
			return version, nil
		}
	}
	out, err := exec.Command("modinfo", "-F", "version", "amneziawg").Output()
	if err != nil {
		return "", fmt.Errorf("cannot confirm AmneziaWG kernel module version")
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("AmneziaWG kernel module did not report a version")
	}
	return version, nil
}

var majorPattern = regexp.MustCompile(`(?:^|[^0-9])v?([0-9]+)(?:\.|$)`)

func parseMajor(version string) int {
	match := majorPattern.FindStringSubmatch(version)
	if len(match) < 2 {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	return major
}
