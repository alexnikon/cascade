package awgcap

import "testing"

func TestParseMajorMinor(t *testing.T) {
	for _, test := range []struct {
		name, output, want string
	}{
		{name: "awg tools", output: "amneziawg-tools v3.1.20260812", want: "3.1"},
		{name: "modinfo", output: "3.1.0-1", want: "3.1"},
		{name: "wireguard tools", output: "wireguard-tools v1.0.20210914", want: "1.0"},
		{name: "garbage", output: "command not found", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseMajorMinor(test.output); got != test.want {
				t.Fatalf("ParseMajorMinor(%q) = %q, want %q", test.output, got, test.want)
			}
		})
	}
}

func TestDetectVersionMismatchUserspaceDoesNotInspectKernel(t *testing.T) {
	t.Setenv("WG_QUICK_USERSPACE_IMPLEMENTATION", "amneziawg-go")
	if got := DetectVersionMismatch(); got != (VersionMismatchReport{}) {
		t.Fatalf("userspace mismatch report = %+v, want zero value", got)
	}
}

func TestDetectPinnedUserspaceSupportsAWG31(t *testing.T) {
	t.Setenv("WG_QUICK_USERSPACE_IMPLEMENTATION", "amneziawg-go")
	got := Detect()
	if !got.AWG3Supported || got.MaxProtocol != "3.1" {
		t.Fatalf("unexpected capability: %+v", got)
	}
	if got.EngineVersion != EngineVersion || got.ToolsVersion != ToolsVersion {
		t.Fatalf("unexpected pinned versions: %+v", got)
	}
}
