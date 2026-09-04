/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestFindsLatestStableReleaseForCurrentPlatform(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeHTTPResponse(t, w, []byte(fmt.Sprintf(`{
			"tag_name":"v2.7.0",
			"draft":false,
			"prerelease":false,
			"assets":[
				{"name":"agents-otel-monitor_v2.7.0_linux-amd64.tar.gz","browser_download_url":%q},
				{"name":"checksums.txt","browser_download_url":%q}
			]
		}`, server.URL+"/archive", server.URL+"/checksums.txt")))
	}))
	t.Cleanup(server.Close)

	client := testUpdater(server.Client(), server.URL, false)
	available, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	if available == nil {
		t.Fatal("check update returned no available release")
	}
	if got, want := available.Version, "v2.7.0"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
}

func TestLatestRejectsHTTPSRedirectToHTTPBeforeFollowing(t *testing.T) {
	insecureRequested := false
	insecureServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		insecureRequested = true
	}))
	t.Cleanup(insecureServer.Close)

	secureServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureServer.URL+"/latest", http.StatusFound)
	}))
	t.Cleanup(secureServer.Close)

	client := testUpdater(secureServer.Client(), secureServer.URL, true)
	_, err := client.Latest(context.Background())
	if err == nil {
		t.Fatal("check followed an HTTPS-to-HTTP redirect")
	}
	if !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("check error = %q, want non-HTTPS rejection", err)
	}
	if insecureRequested {
		t.Fatal("check contacted the insecure redirect target")
	}
}

func TestLatestRejectsNonHTTPSReleaseAssetURLs(t *testing.T) {
	secureServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeHTTPResponse(t, w, []byte(`{
			"tag_name":"v2.7.0",
			"assets":[
				{"name":"agents-otel-monitor_v2.7.0_linux-amd64.tar.gz","browser_download_url":"http://example.invalid/archive"},
				{"name":"checksums.txt","browser_download_url":"https://example.invalid/checksums.txt"}
			]
		}`))
	}))
	t.Cleanup(secureServer.Close)

	client := testUpdater(secureServer.Client(), secureServer.URL, true)
	_, err := client.Latest(context.Background())
	if err == nil {
		t.Fatal("check accepted a non-HTTPS release asset URL")
	}
	if !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("check error = %q, want non-HTTPS rejection", err)
	}
}

func TestLatestRejectsUnsupportedPlatformWithoutNetworkAccess(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported platform must not contact GitHub")
		return nil, nil
	})}

	available, err := newUpdater(updaterOptions{
		httpClient:       client,
		latestReleaseURL: "https://api.github.invalid/latest",
		goos:             "darwin",
		goarch:           "amd64",
		requireHTTPS:     true,
	}).Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "darwin-amd64") {
		t.Fatalf("unsupported platform error = %v, want platform-specific error", err)
	}
	if available != nil {
		t.Fatalf("available = %+v, want nil", available)
	}
}

func TestInstallAtomicallyReplacesExecutableAfterChecksumVerification(t *testing.T) {
	const (
		version     = "v2.7.0"
		archiveName = "agents-otel-monitor_v2.7.0_linux-amd64.tar.gz"
	)
	archive := makeReleaseArchive(t, archiveName, []byte("new executable"))
	digest := sha256.Sum256(archive)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			w.Header().Set("Content-Type", "application/json")
			writeHTTPResponse(t, w, []byte(fmt.Sprintf(`{
				"tag_name":%q,
				"assets":[
					{"name":%q,"browser_download_url":%q},
					{"name":"checksums.txt","browser_download_url":%q}
				]
			}`, version, archiveName, server.URL+"/archive", server.URL+"/checksums.txt")))
		case "/archive":
			writeHTTPResponse(t, w, archive)
		case "/checksums.txt":
			writeHTTPResponse(t, w, []byte(fmt.Sprintf("%x  %s\n", digest, archiveName)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	executablePath := filepath.Join(t.TempDir(), "agents-otel-monitor")
	if err := os.WriteFile(executablePath, []byte("old executable"), 0o750); err != nil {
		t.Fatalf("write current executable: %v", err)
	}

	client := testUpdater(server.Client(), server.URL+"/latest", false)
	available, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	installedPath, err := client.Install(context.Background(), available, filepath.Dir(executablePath))
	if err != nil {
		t.Fatalf("install update: %v", err)
	}
	installedInfo, err := os.Stat(installedPath)
	if err != nil {
		t.Fatalf("stat returned installed path: %v", err)
	}
	executableInfo, err := os.Stat(executablePath)
	if err != nil {
		t.Fatalf("stat requested executable path: %v", err)
	}
	if !filepath.IsAbs(installedPath) || !os.SameFile(installedInfo, executableInfo) {
		t.Fatalf("installed path = %q, want absolute path to requested executable", installedPath)
	}

	got, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read installed executable: %v", err)
	}
	if want := "new executable"; string(got) != want {
		t.Fatalf("installed executable = %q, want %q", got, want)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatalf("stat installed executable: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o750); got != want {
		t.Fatalf("installed permissions = %o, want %o", got, want)
	}
}

func TestInstallLeavesExecutableUntouchedWhenChecksumDoesNotMatch(t *testing.T) {
	const archiveName = "agents-otel-monitor_v2.7.0_linux-amd64.tar.gz"
	archive := makeReleaseArchive(t, archiveName, []byte("untrusted executable"))
	wrongDigest := sha256.Sum256([]byte("different archive"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			writeHTTPResponse(t, w, archive)
		case "/checksums.txt":
			writeHTTPResponse(t, w, []byte(fmt.Sprintf("%x  %s\n", wrongDigest, archiveName)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	executablePath := filepath.Join(t.TempDir(), "agents-otel-monitor")
	if err := os.WriteFile(executablePath, []byte("trusted executable"), 0o755); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	client := testUpdater(server.Client(), server.URL+"/latest", false)
	_, err := client.Install(context.Background(), &Available{
		Version:     "v2.7.0",
		archiveName: archiveName,
		archiveURL:  server.URL + "/archive",
		checksumURL: server.URL + "/checksums.txt",
	}, filepath.Dir(executablePath))
	if err == nil {
		t.Fatal("install update succeeded with a mismatched checksum")
	}

	got, readErr := os.ReadFile(executablePath)
	if readErr != nil {
		t.Fatalf("read current executable: %v", readErr)
	}
	if want := "trusted executable"; string(got) != want {
		t.Fatalf("current executable = %q, want %q", got, want)
	}
}

func TestInstallRejectsUnsafeArchivePathsWithoutReplacingExecutable(t *testing.T) {
	const archiveName = "agents-otel-monitor_v2.7.0_linux-amd64.tar.gz"
	archive := makeArchiveEntry(t, "../agents-otel-monitor", []byte("unsafe executable"))
	digest := sha256.Sum256(archive)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			writeHTTPResponse(t, w, archive)
		case "/checksums.txt":
			writeHTTPResponse(t, w, []byte(fmt.Sprintf("%x  %s\n", digest, archiveName)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	executablePath := filepath.Join(t.TempDir(), "agents-otel-monitor")
	if err := os.WriteFile(executablePath, []byte("current executable"), 0o755); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	client := testUpdater(server.Client(), server.URL+"/latest", false)
	_, err := client.Install(context.Background(), &Available{
		Version:     "v2.7.0",
		archiveName: archiveName,
		archiveURL:  server.URL + "/archive",
		checksumURL: server.URL + "/checksums.txt",
	}, filepath.Dir(executablePath))
	if err == nil {
		t.Fatal("install update accepted an unsafe archive path")
	}

	got, readErr := os.ReadFile(executablePath)
	if readErr != nil {
		t.Fatalf("read current executable: %v", readErr)
	}
	if want := "current executable"; string(got) != want {
		t.Fatalf("current executable = %q, want %q", got, want)
	}
}

func makeReleaseArchive(t *testing.T, archiveName string, executable []byte) []byte {
	t.Helper()
	root := archiveName[:len(archiveName)-len(".tar.gz")]
	return makeArchiveEntry(t, root+"/agents-otel-monitor", executable)
}

func writeHTTPResponse(t *testing.T, w http.ResponseWriter, contents []byte) {
	t.Helper()
	if _, err := w.Write(contents); err != nil {
		t.Errorf("write HTTP test response: %v", err)
	}
}

func testUpdater(httpClient *http.Client, latestReleaseURL string, requireHTTPS bool) *Updater {
	return newUpdater(updaterOptions{
		httpClient:       httpClient,
		latestReleaseURL: latestReleaseURL,
		goos:             "linux",
		goarch:           "amd64",
		requireHTTPS:     requireHTTPS,
	})
}

func makeArchiveEntry(t *testing.T, entryName string, contents []byte) []byte {
	t.Helper()

	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:    entryName,
		Mode:    0o755,
		Size:    int64(len(contents)),
		ModTime: time.Unix(0, 0),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("write archive header: %v", err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatalf("write archive executable: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return compressed.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
