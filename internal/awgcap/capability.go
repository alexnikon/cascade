// Package awgcap detects the maximum AmneziaWG protocol supported by runtime.
package awgcap

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexnikon/cascade/internal/util"
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
	if version := loadedKernelModuleVersion(); version != "" {
		return version, nil
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

func loadedKernelModuleVersion() string {
	data, err := os.ReadFile("/sys/module/amneziawg/version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// VersionMismatchReport contains best-effort runtime version diagnostics.
// Empty values mean that the corresponding version could not be detected.
type VersionMismatchReport struct {
	CLIVersion             string
	LoadedKernelVersion    string
	InstalledKernelVersion string
	Mismatch               bool
}

var versionPattern = regexp.MustCompile(`v?(\d+\.\d+)`)

// ParseMajorMinor extracts the major.minor compatibility line from awg or
// modinfo output. Patch and distribution suffixes are intentionally ignored.
func ParseMajorMinor(output string) string {
	match := versionPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// DetectCLIVersion returns the installed awg-tools major.minor version.
func DetectCLIVersion() string {
	out, err := util.ExecSilent("awg --version")
	if err != nil {
		return ""
	}
	return ParseMajorMinor(out)
}

// DetectInstalledKernelModuleVersion returns the module version visible to
// modinfo, or an empty string when the module is unavailable.
func DetectInstalledKernelModuleVersion() string {
	out, err := util.ExecSilent("modinfo -F version amneziawg")
	if err != nil {
		return ""
	}
	return ParseMajorMinor(out)
}

// DetectVersionMismatch compares the CLI with the loaded kernel module, or
// with the installed module when it is not currently loaded. A mismatch is
// reported only when both values are known.
func DetectVersionMismatch() VersionMismatchReport {
	if os.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION") == "amneziawg-go" {
		return VersionMismatchReport{}
	}

	report := VersionMismatchReport{CLIVersion: DetectCLIVersion()}
	report.LoadedKernelVersion = ParseMajorMinor(loadedKernelModuleVersion())
	report.InstalledKernelVersion = DetectInstalledKernelModuleVersion()
	kernelVersion := report.LoadedKernelVersion
	if kernelVersion == "" {
		kernelVersion = report.InstalledKernelVersion
	}
	report.Mismatch = report.CLIVersion != "" && kernelVersion != "" && report.CLIVersion != kernelVersion
	return report
}

// LogVersionCompatibility emits non-blocking kernel-mode compatibility
// diagnostics at startup. Userspace mode deliberately performs no kernel
// detection and produces no warning.
func LogVersionCompatibility() {
	if os.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION") == "amneziawg-go" {
		return
	}
	report := DetectVersionMismatch()
	if report.CLIVersion == "" && report.LoadedKernelVersion == "" && report.InstalledKernelVersion == "" {
		return
	}
	log.Printf("awg compatibility: cli=%s loaded-kernel=%s installed-kernel=%s",
		report.CLIVersion, report.LoadedKernelVersion, report.InstalledKernelVersion)
	if report.Mismatch {
		log.Printf("WARNING: awg CLI/kernel module compatibility mismatch: cli=%s kernel=%s; run deploy/switch-mode.sh --kernel before starting AWG3 interfaces",
			report.CLIVersion, report.LoadedKernelVersion)
	}
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
