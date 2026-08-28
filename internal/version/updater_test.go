package version

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func updateResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func withUpdateServer(t *testing.T, transport roundTripFunc, currentVersion string) {
	t.Helper()

	oldURL := latestReleaseURL
	oldCompareURL := compareReleaseURL
	oldClient := updateHTTPClient
	oldNow := nowUTC
	oldVersion := Version
	oldGitCommit := GitCommit
	oldStatus := GetStatus()
	t.Cleanup(func() {
		latestReleaseURL = oldURL
		compareReleaseURL = oldCompareURL
		updateHTTPClient = oldClient
		nowUTC = oldNow
		Version = oldVersion
		GitCommit = oldGitCommit
		mu.Lock()
		status = oldStatus
		mu.Unlock()
	})

	latestReleaseURL = "https://api.example.invalid/repos/cascade/releases/latest"
	compareReleaseURL = "https://api.example.invalid/repos/cascade/compare/"
	updateHTTPClient = &http.Client{Transport: transport}
	nowUTC = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }
	Version = currentVersion
	GitCommit = "unknown"
	mu.Lock()
	status = UpdateStatus{}
	mu.Unlock()
}

func TestCheckUsesCommitAncestryForDevelopmentBuilds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       string
		available    bool
		updateStatus string
	}{
		{name: "ahead", status: "ahead", available: false, updateStatus: UpdateStatusCurrent},
		{name: "identical", status: "identical", available: false, updateStatus: UpdateStatusCurrent},
		{name: "behind", status: "behind", available: true, updateStatus: UpdateStatusAvailable},
		{name: "diverged", status: "diverged", available: true, updateStatus: UpdateStatusAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			withUpdateServer(t, func(r *http.Request) (*http.Response, error) {
				requests++
				switch r.URL.Path {
				case "/repos/cascade/releases/latest":
					return updateResponse(http.StatusOK, `{"tag_name":"v1.3.0","html_url":"https://example.invalid/release"}`), nil
				case "/repos/cascade/compare/v1.3.0...abcdef0123456789":
					return updateResponse(http.StatusOK, `{"status":"`+tc.status+`"}`), nil
				default:
					t.Fatalf("unexpected request path: %s", r.URL.Path)
					return nil, nil
				}
			}, "dev")
			GitCommit = "abcdef0123456789"

			check()
			got := GetStatus()
			if got.UpdateAvailable != tc.available || got.UpdateStatus != tc.updateStatus || got.Error != "" {
				t.Fatalf("unexpected status: %+v", got)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestCheckTreatsBuildWithoutVersionMetadataAsUnknown(t *testing.T) {
	requests := 0
	withUpdateServer(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return updateResponse(http.StatusOK, `{"tag_name":"v1.3.0","html_url":"https://example.invalid/release"}`), nil
	}, "dev")
	GitCommit = "not-a-sha"

	check()
	got := GetStatus()
	if got.UpdateAvailable || got.UpdateStatus != UpdateStatusUnknown || got.Error != "" {
		t.Fatalf("unexpected status: %+v", got)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestCheckSuppressesUpdateWhenCommitComparisonFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		body       string
		errorPart  string
	}{
		{name: "http", statusCode: http.StatusServiceUnavailable, body: "unavailable", errorPart: "503"},
		{name: "decode", statusCode: http.StatusOK, body: "not-json", errorPart: "decode"},
		{name: "unknown status", statusCode: http.StatusOK, body: `{"status":"unknown"}`, errorPart: "unexpected status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withUpdateServer(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/repos/cascade/releases/latest" {
					return updateResponse(http.StatusOK, `{"tag_name":"v1.3.0","html_url":"https://example.invalid/release","body":"Changes"}`), nil
				}
				return updateResponse(tc.statusCode, tc.body), nil
			}, "dev")
			GitCommit = "abcdef0123456789"

			check()
			got := GetStatus()
			if got.UpdateAvailable || got.UpdateStatus != UpdateStatusUnknown || !strings.Contains(got.Error, tc.errorPart) {
				t.Fatalf("unexpected status: %+v", got)
			}
			if got.LatestVersion != "v1.3.0" || got.ReleaseURL == "" || got.Changelog != "Changes" || got.CheckedAt.IsZero() {
				t.Fatalf("release metadata not retained: %+v", got)
			}
		})
	}
}

func TestCheckLatestRelease(t *testing.T) {
	withUpdateServer(t, func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("User-Agent"); got != "cascade-update-checker/v1.2.3" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		return updateResponse(http.StatusOK, `{"tag_name":"v1.3.0","html_url":"https://releases.example/v1.3.0","body":"Portable updates"}`), nil
	}, "v1.2.3")

	check()
	got := GetStatus()
	if got.LatestVersion != "v1.3.0" || !got.UpdateAvailable || got.UpdateStatus != UpdateStatusAvailable {
		t.Fatalf("unexpected status: %+v", got)
	}
	if got.ReleaseURL != "https://releases.example/v1.3.0" || got.Changelog != "Portable updates" {
		t.Errorf("ReleaseURL = %q", got.ReleaseURL)
	}
	if got.Error != "" || got.CheckedAt.IsZero() {
		t.Errorf("unexpected error/timestamp: %+v", got)
	}
}

func TestCheckEqualReleaseIsCurrent(t *testing.T) {
	withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
		return updateResponse(http.StatusOK, `{"tag_name":"v1.2.3","html_url":"https://example.invalid/release"}`), nil
	}, "v1.2.3-4-gabcdef0")

	check()
	if got := GetStatus(); got.UpdateAvailable || got.UpdateStatus != UpdateStatusCurrent || got.LatestVersion != "v1.2.3" || got.Error != "" {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestCheckNewerDevelopmentVersionIsCurrentWithoutCommitMetadata(t *testing.T) {
	withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
		return updateResponse(http.StatusOK, `{"tag_name":"v1.2.3","html_url":"https://example.invalid/release"}`), nil
	}, "v1.2.4-2-gabcdef0")

	check()
	if got := GetStatus(); got.UpdateAvailable || got.UpdateStatus != UpdateStatusCurrent || got.Error != "" {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestCheckReportsHTTPAndDecodeErrors(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
			return updateResponse(http.StatusServiceUnavailable, "unavailable"), nil
		}, "v1.2.3")
		check()
		if got := GetStatus(); got.UpdateStatus != UpdateStatusUnknown || !strings.Contains(got.Error, "503") || got.CheckedAt.IsZero() {
			t.Fatalf("unexpected status: %+v", got)
		}
	})

	t.Run("decode", func(t *testing.T) {
		withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
			return updateResponse(http.StatusOK, "not-json"), nil
		}, "v1.2.3")
		check()
		if got := GetStatus(); !strings.Contains(got.Error, "decode") || got.CheckedAt.IsZero() {
			t.Fatalf("unexpected status: %+v", got)
		}
	})
}

func TestCheckReportsTimeout(t *testing.T) {
	withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}, "v1.2.3")
	check()
	if got := GetStatus(); !strings.Contains(got.Error, "deadline exceeded") {
		t.Fatalf("timeout status: %+v", got)
	}
}

func TestSuccessfulCheckClearsPreviousError(t *testing.T) {
	withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
		return updateResponse(http.StatusOK, `{"tag_name":"v1.2.3","html_url":"https://example.invalid/release"}`), nil
	}, "v1.2.3")
	mu.Lock()
	status = UpdateStatus{Error: "temporary failure"}
	mu.Unlock()

	check()
	if got := GetStatus(); got.Error != "" {
		t.Fatalf("successful check retained error: %+v", got)
	}
}

func TestCheckRejectsMalformedRelease(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"latest"}`,
		`{"tag_name":"v1.3.0","html_url":"javascript:alert(1)"}`,
	} {
		withUpdateServer(t, func(_ *http.Request) (*http.Response, error) {
			return updateResponse(http.StatusOK, body), nil
		}, "v1.2.3")
		check()
		if got := GetStatus(); !strings.Contains(got.Error, "validate") {
			t.Fatalf("malformed release status: %+v", got)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.2.3", "v1.2.4", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.9.9", "v2.0.0", -1},
		{"v1.2.3", "1.2.3", 0},      // with/without v prefix
		{"v1.2.3-rc1", "v1.2.3", 0}, // pre-release suffix stripped → equal
		{"v1.3.0-alpha", "v1.2.9", 1},
		{"dev", "v1.0.0", -1}, // dev builds are 0.0.0
		{"v0.0.0", "dev", 0},
		{"v1.0.0", "v1.0.0", 0},
		{"v10.0.0", "v9.9.9", 1}, // double-digit major
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
	}{
		{"v1.2.3", [3]int{1, 2, 3}},
		{"1.2.3", [3]int{1, 2, 3}},
		{"v2.0.0-rc1", [3]int{2, 0, 0}},
		{"dev", [3]int{0, 0, 0}},
		{"", [3]int{0, 0, 0}},
		{"v10.20.30", [3]int{10, 20, 30}},
	}
	for _, tt := range tests {
		got := parseSemver(tt.in)
		if got != tt.want {
			t.Errorf("parseSemver(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
