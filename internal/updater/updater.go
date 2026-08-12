/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

// Package updater discovers and installs official claude-code-monitor releases.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"

	"golang.org/x/mod/semver"
)

const latestReleaseEndpoint = "https://api.github.com/repos/kurok1/claude-code-monitor/releases/latest"

// Available describes a newer release and the assets required to install it.
type Available struct {
	Version string

	archiveName string
	archiveURL  string
	checksumURL string
}

// Updater checks GitHub Releases for a binary matching one platform.
type Updater struct {
	httpClient       *http.Client
	latestReleaseURL string
	goos             string
	goarch           string
	requireHTTPS     bool
}

type updaterOptions struct {
	httpClient       *http.Client
	latestReleaseURL string
	goos             string
	goarch           string
	requireHTTPS     bool
}

type releaseResponse struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// New returns an updater for the current runtime platform.
func New() *Updater {
	return newUpdater(updaterOptions{
		httpClient:       http.DefaultClient,
		latestReleaseURL: latestReleaseEndpoint,
		goos:             runtime.GOOS,
		goarch:           runtime.GOARCH,
		requireHTTPS:     true,
	})
}

func newUpdater(options updaterOptions) *Updater {
	return &Updater{
		httpClient:       options.httpClient,
		latestReleaseURL: options.latestReleaseURL,
		goos:             options.goos,
		goarch:           options.goarch,
		requireHTTPS:     options.requireHTTPS,
	}
}

// Check returns the latest stable release when it is newer than currentVersion.
// A nil result means the current version is already latest or the platform is
// not distributed as an official binary.
func (u *Updater) Check(ctx context.Context, currentVersion string) (*Available, error) {
	if currentVersion == "dev" {
		return nil, nil
	}
	target, ok := releaseTarget(u.goos, u.goarch)
	if !ok {
		return nil, nil
	}
	if !semver.IsValid(currentVersion) {
		return nil, fmt.Errorf("current version %q is not valid semver", currentVersion)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.latestReleaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "claude-code-monitor/"+currentVersion)

	resp, err := u.do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close() // A close error cannot affect a fully decoded read-only response.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch latest release: unexpected HTTP status %s", resp.Status)
	}

	var release releaseResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode latest release: %w", err)
	}
	if release.Draft || release.Prerelease || semver.Prerelease(release.TagName) != "" {
		return nil, fmt.Errorf("latest release %q is not stable", release.TagName)
	}
	if !semver.IsValid(release.TagName) {
		return nil, fmt.Errorf("latest release tag %q is not valid semver", release.TagName)
	}
	if semver.Compare(release.TagName, currentVersion) <= 0 {
		return nil, nil
	}

	archiveName := fmt.Sprintf("claude-code-monitor_%s_%s.tar.gz", release.TagName, target)
	available := &Available{Version: release.TagName, archiveName: archiveName}
	for _, asset := range release.Assets {
		switch asset.Name {
		case archiveName:
			available.archiveURL = asset.URL
		case "checksums.txt":
			available.checksumURL = asset.URL
		}
	}
	if available.archiveURL == "" {
		return nil, fmt.Errorf("release %s has no %s asset", release.TagName, archiveName)
	}
	if available.checksumURL == "" {
		return nil, fmt.Errorf("release %s has no checksums.txt asset", release.TagName)
	}
	if err := u.validateURLString(available.archiveURL); err != nil {
		return nil, fmt.Errorf("validate %s asset URL: %w", archiveName, err)
	}
	if err := u.validateURLString(available.checksumURL); err != nil {
		return nil, fmt.Errorf("validate checksums.txt asset URL: %w", err)
	}
	return available, nil
}

func (u *Updater) do(req *http.Request) (*http.Response, error) {
	if err := u.validateURL(req.URL); err != nil {
		return nil, err
	}

	client := *u.httpClient
	configuredCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if configuredCheckRedirect != nil {
			if err := configuredCheckRedirect(redirect, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return u.validateURL(redirect.URL)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if err := u.validateURL(resp.Request.URL); err != nil {
		_ = resp.Body.Close() // The rejected response body must not be consumed.
		return nil, fmt.Errorf("validate final response URL: %w", err)
	}
	return resp, nil
}

func (u *Updater) validateURLString(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	return u.validateURL(parsed)
}

func (u *Updater) validateURL(target *url.URL) error {
	if !u.requireHTTPS {
		return nil
	}
	if target.Scheme != "https" || target.Host == "" {
		return fmt.Errorf("refuse non-HTTPS URL %q", target.Redacted())
	}
	return nil
}

func releaseTarget(goos, goarch string) (string, bool) {
	switch {
	case goos == "linux" && goarch == "amd64":
		return "linux-amd64", true
	case goos == "linux" && goarch == "arm64":
		return "linux-arm64", true
	case goos == "darwin" && goarch == "arm64":
		return "darwin-arm64", true
	default:
		return "", false
	}
}
