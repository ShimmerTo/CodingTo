package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// appVersion is the version of the CodingTo client itself. It is kept in sync
// with the value reported by GetBootstrap so the UI and the update check agree.
const appVersion = "0.1.12"

// appUpdateRepo is the GitHub repository that publishes client releases.
const appUpdateRepo = "ShimmerTo/CodingTo"

// githubRelease mirrors the subset of the GitHub "release" payload we need.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Body    string        `json:"body"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
	Size               int64  `json:"size"`
}

// AppUpdateStatus is returned to the frontend by CheckAppUpdate.
type AppUpdateStatus struct {
	Current     string `json:"current"`         // running client version
	Latest      string `json:"latest"`          // latest release tag (no leading v)
	HasNewer    bool   `json:"hasNewer"`        // latest release is higher than current
	Available   bool   `json:"available"`       // a newer release exists; user opens the release page to download
	DownloadURL string `json:"downloadUrl"`     // GitHub release page URL the UI opens
	Notes       string `json:"notes"`           // release notes (changelog)
	Error       string `json:"error,omitempty"` // non-empty when the check failed
}

// CheckAppUpdate queries the GitHub latest release for the CodingTo client and
// reports whether a newer version exists. Update detection is purely version
// based: if the latest release tag is higher than the running version, the user
// is pointed to the release page and downloads manually — no platform/asset
// guessing is involved, so variant builds (e.g. a WebView-bundled "-full") keep
// working without touching this logic.
func (a *App) CheckAppUpdate() AppUpdateStatus {
	status := AppUpdateStatus{
		Current: appVersion,
	}

	release, statusCode, err := fetchLatestRelease(appUpdateRepo)
	if err != nil {
		// A 404 means there is no published release yet. Treat that as
		// "nothing to update to" rather than surfacing an error.
		if statusCode == http.StatusNotFound {
			return status
		}
		status.Error = err.Error()
		return status
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	status.Latest = latest
	status.Notes = release.Body
	status.HasNewer = compareVersions(latest, appVersion) > 0
	// Newer release => let the user open the GitHub release page and pick the
	// build that fits them. DownloadURL is the page URL the UI opens.
	status.DownloadURL = release.HTMLURL
	status.Available = status.HasNewer
	return status
}

// fetchLatestRelease calls the GitHub API for the latest non-draft, non-prerelease.
func fetchLatestRelease(repo string) (githubRelease, int, error) {
	endpoint := "https://api.github.com/repos/" + repo + "/releases/latest"
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return githubRelease{}, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CodingTo-App-Update-Checker")

	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, resp.StatusCode, fmt.Errorf("GitHub API 返回状态码 %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, resp.StatusCode, err
	}
	return release, resp.StatusCode, nil
}

// compareVersions compares two dotted version strings (major.minor.patch).
// It returns -1 if a < b, 1 if a > b, and 0 if equal.
func compareVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	for i := 0; i < 3; i++ {
		if as[i] != bs[i] {
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	out := [3]int{}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			n = 0
		}
		out[i] = n
	}
	return out
}
