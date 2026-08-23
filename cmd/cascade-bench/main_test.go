package main

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	values := []time.Duration{
		100 * time.Millisecond,
		10 * time.Millisecond,
		50 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	}
	got := summarize(values)
	if got.Min != 10 || got.P50 != 30 || got.P95 != 50 || got.Max != 100 || got.Avg != 42 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}
