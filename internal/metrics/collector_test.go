package metrics

import (
	"os"
	"testing"
)

func TestParseHistoryEnabled(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "true", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "0", want: false},
		{value: "OFF", want: false},
	} {
		if got := parseHistoryEnabled(test.value); got != test.want {
			t.Fatalf("parseHistoryEnabled(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestParseProcessStat(t *testing.T) {
	content := "123 (amneziawg-go worker) S 1 2 3 4 5 6 7 8 9 10 120 30 14 15 16 17 18 19 20 21 7"
	name, cpu, rss, err := parseProcessStat(content)
	if err != nil {
		t.Fatalf("parseProcessStat: %v", err)
	}
	if name != "amneziawg-go worker" {
		t.Fatalf("name = %q", name)
	}
	if cpu != 150 {
		t.Fatalf("cpu ticks = %d, want 150", cpu)
	}
	if want := int64(7 * os.Getpagesize()); rss != want {
		t.Fatalf("rss = %d, want %d", rss, want)
	}
}

func TestProcessCPUPercentRequiresPreviousSample(t *testing.T) {
	if got := processCPUPercent(5000, 0, false, 100, 4); got != 0 {
		t.Fatalf("first sample CPU = %f, want 0", got)
	}
	if got := processCPUPercent(120, 100, true, 80, 4); got != 100 {
		t.Fatalf("delta CPU = %f, want 100", got)
	}
}
