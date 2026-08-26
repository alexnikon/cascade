package version

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	checkInterval = 24 * time.Hour
	httpTimeout   = 10 * time.Second
	// Delay first check so the container has time to come fully online.
	initialDelay = 10 * time.Second
)

// UpdateStatus is the cached result of the last update check.
type UpdateStatus struct {
	LatestVersion   string    `json:"latestVersion"`
	ReleaseURL      string    `json:"releaseURL"`
	Changelog       string    `json:"changelog,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	CheckedAt       time.Time `json:"checkedAt"`
	Error           string    `json:"error,omitempty"`
}

var (
	mu                sync.RWMutex
	status            UpdateStatus
	latestReleaseURL  = "https://api.github.com/repos/alexnikon/cascade/releases/latest"
	compareReleaseURL = "https://api.github.com/repos/alexnikon/cascade/compare/"
	updateHTTPClient  = http.DefaultClient
	nowUTC            = func() time.Time { return time.Now().UTC() }
)

var semanticVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z.-]+)?$`)
var gitCommit = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

type releasePayload struct {
	Version    string `json:"tag_name"`
	ReleaseURL string `json:"html_url"`
	Changelog  string `json:"body"`
}

// GetStatus returns the latest cached UpdateStatus (safe for concurrent use).
func GetStatus() UpdateStatus {
	mu.RLock()
	defer mu.RUnlock()
	return status
}

// Start launches the background update-check goroutine.
// It checks immediately after initialDelay, then every checkInterval.
// Safe to call multiple times — only the first call has effect.
func Start() {
	go func() {
		time.Sleep(initialDelay)
		check()
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for range ticker.C {
			check()
		}
	}()
}

// Check forces an immediate update check, bypassing the 24h cache.
// Safe to call concurrently — it runs synchronously and updates the shared status.
func Check() {
	check()
}

// check fetches the latest GitHub release and updates the cached status.
func check() {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		setError(fmt.Sprintf("build request: %v", err))
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cascade-update-checker/"+Version)

	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		setError(fmt.Sprintf("http: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		setError(fmt.Sprintf("latest release returned %d", resp.StatusCode))
		return
	}

	var payload releasePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		setError(fmt.Sprintf("decode: %v", err))
		return
	}

	if !semanticVersion.MatchString(payload.Version) {
		setError("validate: latest release requires a semantic version tag")
		return
	}
	if payload.ReleaseURL != "" {
		releaseURL, err := url.Parse(payload.ReleaseURL)
		if err != nil || releaseURL.Scheme != "https" || releaseURL.Host == "" {
			setError("validate: releaseURL must be an absolute HTTPS URL")
			return
		}
	}
	available := compareSemver(payload.Version, Version) > 0
	if available && gitCommit.MatchString(GitCommit) {
		comparison, err := compareReleaseCommit(ctx, payload.Version, GitCommit)
		if err != nil {
			setReleaseError(payload, fmt.Sprintf("compare: %v", err))
			return
		}
		switch comparison {
		case "ahead", "identical":
			available = false
		case "behind", "diverged":
			// The running commit does not contain the latest release commit.
		default:
			setReleaseError(payload, fmt.Sprintf("compare: unexpected status %q", comparison))
			return
		}
	}
	log.Printf("version: update check — current=%s latest=%s updateAvailable=%v",
		Version, payload.Version, available)

	mu.Lock()
	status = UpdateStatus{
		LatestVersion:   payload.Version,
		ReleaseURL:      payload.ReleaseURL,
		Changelog:       payload.Changelog,
		UpdateAvailable: available,
		CheckedAt:       nowUTC(),
	}
	mu.Unlock()
}

func compareReleaseCommit(ctx context.Context, releaseTag, currentCommit string) (string, error) {
	compareURL := compareReleaseURL + url.PathEscape(releaseTag) + "..." + url.PathEscape(currentCommit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, compareURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cascade-update-checker/"+Version)

	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	return payload.Status, nil
}

func setReleaseError(payload releasePayload, msg string) {
	log.Printf("version: update check failed: %s", msg)
	mu.Lock()
	status = UpdateStatus{
		LatestVersion:   payload.Version,
		ReleaseURL:      payload.ReleaseURL,
		Changelog:       payload.Changelog,
		UpdateAvailable: false,
		CheckedAt:       nowUTC(),
		Error:           msg,
	}
	mu.Unlock()
}

func setError(msg string) {
	log.Printf("version: update check failed: %s", msg)
	mu.Lock()
	status.Error = msg
	status.CheckedAt = nowUTC()
	mu.Unlock()
}

// compareSemver returns:
//
//	-1  if a < b
//	 0  if a == b
//	+1  if a > b
//
// Handles "v1.2.3", "1.2.3", "v1.2.3-rc1" (pre-release suffix stripped).
// Non-parseable or dev-mode versions ("dev") are treated as 0.0.0.
func compareSemver(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	// Strip pre-release suffix (e.g. "-rc1", "-alpha")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		v = v[:idx]
	}
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		out[i], _ = strconv.Atoi(parts[i])
	}
	return out
}
