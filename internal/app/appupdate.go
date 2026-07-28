package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// appVersion is the version of the CodingTo client itself. It is kept in sync
// with the value reported by GetBootstrap so the UI and the update check agree.
const appVersion = "0.1.1"

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
	Available   bool   `json:"available"`       // newer release AND a matching asset exists
	AssetName   string `json:"assetName"`       // matched asset file name
	DownloadURL string `json:"downloadUrl"`     // matched asset download URL
	Notes       string `json:"notes"`           // release notes (changelog)
	Platform    string `json:"platform"`        // detected os/arch
	Error       string `json:"error,omitempty"` // non-empty when the check failed
}

// CheckAppUpdate queries the GitHub latest release for the CodingTo client and
// reports whether a newer version with an asset for the current platform exists.
func (a *App) CheckAppUpdate() AppUpdateStatus {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	status := AppUpdateStatus{
		Current:  appVersion,
		Platform: fmt.Sprintf("%s/%s", goos, goarch),
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

	asset, ok := matchPlatformAsset(release.Assets, goos, goarch)
	if ok {
		status.AssetName = asset.Name
		status.DownloadURL = asset.BrowserDownloadURL
	}
	// A "download and install" button only makes sense when there is both a
	// newer version and an installable asset for this platform.
	status.Available = ok && status.HasNewer
	return status
}

// DownloadAndInstallApp downloads the given release asset to a temporary file
// and opens it with the operating system's default handler. Installers
// (.exe/.msi/.dmg) launch automatically; archives open for the user to extract.
func (a *App) DownloadAndInstallApp(downloadURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("下载地址为空")
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	tmpDir, err := os.MkdirTemp("", "codingto-update")
	if err != nil {
		return err
	}
	ext := filepath.Ext(downloadURL)
	if u, perr := url.Parse(downloadURL); perr == nil {
		if e := filepath.Ext(u.Path); e != "" {
			ext = e
		}
	}
	if ext == "" {
		ext = ".tmp"
	}
	tmpPath := filepath.Join(tmpDir, "codingto-update"+ext)

	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return openFile(tmpPath)
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

// matchPlatformAsset returns the asset for the running os/arch. It first keeps
// assets whose name implies the current platform, then prefers an exact arch
// match, falling back to the first platform candidate.
func matchPlatformAsset(assets []githubAsset, goos, goarch string) (githubAsset, bool) {
	var candidates []githubAsset
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		switch goos {
		case "windows":
			if (strings.Contains(name, "windows") || strings.Contains(name, "win")) &&
				(strings.Contains(name, goarch) ||
					(goarch == "amd64" && (strings.Contains(name, "x64") || strings.Contains(name, "x86_64")))) {
				candidates = append(candidates, asset)
			}
		case "darwin":
			if strings.Contains(name, "darwin") || strings.Contains(name, "macos") || strings.Contains(name, "mac") {
				candidates = append(candidates, asset)
			}
		default:
			if strings.Contains(name, goos) {
				candidates = append(candidates, asset)
			}
		}
	}
	if len(candidates) == 0 {
		return githubAsset{}, false
	}
	for _, asset := range candidates {
		if strings.Contains(strings.ToLower(asset.Name), goarch) {
			return asset, true
		}
	}
	return candidates[0], true
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

// openFile opens a file with the operating system's default handler.
func openFile(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}
