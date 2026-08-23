package awgcap

import "testing"

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
