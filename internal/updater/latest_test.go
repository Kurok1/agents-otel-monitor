/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.1
 */

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLatestSelectsMatchingAssetOnEveryInvocation(t *testing.T) {
	for _, platform := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		t.Run(platform.goos+"-"+platform.goarch, func(t *testing.T) {
			var requests atomic.Int32
			release := releaseResponse{TagName: "v3.0.1", Assets: make([]releaseAsset, 0, 4)}
			for _, target := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
				name := "agents-otel-monitor_v3.0.1_" + target + ".tar.gz"
				release.Assets = append(release.Assets, releaseAsset{Name: name, URL: "https://example.invalid/" + name})
			}
			release.Assets = append(release.Assets, releaseAsset{Name: "checksums.txt", URL: "https://example.invalid/checksums.txt"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Error("missing GitHub API Accept header")
				}
				if err := json.NewEncoder(w).Encode(release); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			client := newUpdater(updaterOptions{httpClient: server.Client(), latestReleaseURL: server.URL, goos: platform.goos, goarch: platform.goarch})
			for range 2 {
				available, err := client.Latest(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("agents-otel-monitor_v3.0.1_%s-%s.tar.gz", platform.goos, platform.goarch)
				if available.Version != "v3.0.1" || available.archiveName != want || available.archiveURL != "https://example.invalid/"+want {
					t.Fatalf("release = %+v, want %s", available, want)
				}
			}
			if requests.Load() != 2 {
				t.Fatalf("requests = %d, want fresh lookup on each invocation", requests.Load())
			}
		})
	}
}

func TestLatestRejectsInvalidReleaseResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{name: "network failure", status: http.StatusServiceUnavailable, want: "503"},
		{name: "invalid JSON", body: "{", want: "decode"},
		{name: "draft", body: `{"tag_name":"v3.0.1","draft":true}`, want: "not stable"},
		{name: "prerelease", body: `{"tag_name":"v3.0.1","prerelease":true}`, want: "not stable"},
		{name: "prerelease tag", body: `{"tag_name":"v3.0.1-rc.1"}`, want: "not stable"},
		{name: "invalid tag", body: `{"tag_name":"invalid"}`, want: "semver"},
		{name: "missing archive", body: `{"tag_name":"v3.0.1","assets":[]}`, want: "linux-amd64.tar.gz"},
		{name: "missing checksum", body: `{"tag_name":"v3.0.1","assets":[{"name":"agents-otel-monitor_v3.0.1_linux-amd64.tar.gz","browser_download_url":"https://example.invalid/archive"}]}`, want: "checksums.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				writeHTTPResponse(t, w, []byte(tc.body))
			}))
			t.Cleanup(server.Close)
			_, err := testUpdater(server.Client(), server.URL, false).Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLatestPrefersCurrentNameAndAcceptsLegacyAssets(t *testing.T) {
	const (
		current = "agents-otel-monitor_v3.0.1_linux-amd64.tar.gz"
		legacy  = "claude-code-monitor_v3.0.1_linux-amd64.tar.gz"
	)
	currentAsset := releaseAsset{Name: current, URL: "https://example.invalid/" + current}
	legacyAsset := releaseAsset{Name: legacy, URL: "https://example.invalid/" + legacy}
	for _, tc := range []struct {
		name    string
		assets  []releaseAsset
		want    string
		wantErr string
	}{
		{name: "current only", assets: []releaseAsset{currentAsset}, want: current},
		{name: "legacy only", assets: []releaseAsset{legacyAsset}, want: legacy},
		{name: "current first", assets: []releaseAsset{currentAsset, legacyAsset}, want: current},
		{name: "legacy first", assets: []releaseAsset{legacyAsset, currentAsset}, want: current},
		{name: "invalid current URL", assets: []releaseAsset{legacyAsset, {Name: current, URL: "http://example.invalid/archive"}}, wantErr: "non-HTTPS"},
		{name: "empty current URL", assets: []releaseAsset{legacyAsset, {Name: current}}, wantErr: current},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assets := make([]releaseAsset, 0, len(tc.assets)+1)
			assets = append(assets, tc.assets...)
			assets = append(assets, releaseAsset{Name: "checksums.txt", URL: "https://example.invalid/checksums.txt"})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(releaseResponse{TagName: "v3.0.1", Assets: assets}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			available, err := testUpdater(server.Client(), server.URL, true).Latest(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if available.archiveName != tc.want || available.archiveURL != "https://example.invalid/"+tc.want {
				t.Fatalf("selected release = %+v, want %s", available, tc.want)
			}
		})
	}
}
