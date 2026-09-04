/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.1
 */

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallIntoDirectoryAndReinstall(t *testing.T) {
	const archiveName = "agents-otel-monitor_v3.0.1_linux-amd64.tar.gz"
	archive := makeReleaseArchive(t, archiveName, []byte("release executable"))
	client, release := installationFixture(t, archiveName, archive)
	dir := filepath.Join(t.TempDir(), "new", "install")
	for attempt := range 2 {
		installed, err := client.Install(context.Background(), release, dir)
		if err != nil {
			t.Fatalf("install attempt %d: %v", attempt, err)
		}
		if want := filepath.Join(dir, "agents-otel-monitor"); installed != want {
			t.Fatalf("installed path = %q, want %q", installed, want)
		}
		assertFileContents(t, installed, "release executable")
		info, err := os.Stat(installed)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %o, want 755", info.Mode().Perm())
		}
		assertNoInstallTemps(t, dir)
	}
}

func TestInstallPreservesOtherFilesAndOpenExecutable(t *testing.T) {
	const archiveName = "agents-otel-monitor_v3.0.1_linux-amd64.tar.gz"
	client, release := installationFixture(t, archiveName, makeReleaseArchive(t, archiveName, []byte("new binary")))
	dir := t.TempDir()
	target := filepath.Join(dir, "agents-otel-monitor")
	if err := os.WriteFile(target, []byte("running binary"), 0o750); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close() // Read-only fixture descriptor.
	for _, name := range []string{"config.yaml", "monitor.duckdb", "litellm.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Install(context.Background(), release, dir); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, target, "new binary")
	oldBytes := make([]byte, len("running binary"))
	if _, err := previous.ReadAt(oldBytes, 0); err != nil {
		t.Fatal(err)
	}
	if string(oldBytes) != "running binary" {
		t.Fatalf("open executable changed: %q", oldBytes)
	}
	for _, name := range []string{"config.yaml", "monitor.duckdb", "litellm.json"} {
		assertFileContents(t, filepath.Join(dir, name), "keep me")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %o, want 750", info.Mode().Perm())
	}
}

func TestInstallRejectsInvalidTargetsBeforeDownload(t *testing.T) {
	for _, kind := range []string{"empty", "directory", "symlink", "dangling symlink", "install dir is file"} {
		t.Run(kind, func(t *testing.T) {
			client := testUpdater(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("invalid target must not download assets")
				return nil, fmt.Errorf("unexpected network access")
			})}, "https://example.invalid/latest", true)
			dir := t.TempDir()
			target := filepath.Join(dir, "agents-otel-monitor")
			switch kind {
			case "empty":
				dir = ""
			case "directory":
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling symlink":
				outside := filepath.Join(t.TempDir(), "external")
				if kind == "symlink" {
					if err := os.WriteFile(outside, []byte("external binary"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "install dir is file":
				if err := os.WriteFile(target, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				dir = target
			}
			if _, err := client.Install(context.Background(), &Available{Version: "v3.0.1"}, dir); err == nil {
				t.Fatal("install accepted an invalid target")
			}
		})
	}
}

func TestInstallFailurePreservesExistingFileAndCleansTemps(t *testing.T) {
	const archiveName = "agents-otel-monitor_v3.0.1_linux-amd64.tar.gz"
	for _, kind := range []string{"download failure", "invalid gzip", "missing executable", "unsafe path", "empty executable", "cancelled", "unwritable directory"} {
		t.Run(kind, func(t *testing.T) {
			archive := makeReleaseArchive(t, archiveName, []byte("new binary"))
			switch kind {
			case "invalid gzip":
				archive = []byte("not gzip")
			case "missing executable":
				archive = makeArchiveEntry(t, "config.example.yaml", []byte("example"))
			case "unsafe path":
				archive = makeArchiveEntry(t, "../agents-otel-monitor", []byte("unsafe"))
			case "empty executable":
				archive = makeReleaseArchive(t, archiveName, nil)
			}
			client, release := installationFixture(t, archiveName, archive)
			if kind == "download failure" {
				release.archiveURL = strings.TrimSuffix(release.archiveURL, "/archive") + "/missing"
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "agents-otel-monitor")
			if err := os.WriteFile(target, []byte("keep binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			if kind == "unwritable directory" {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory write permissions")
				}
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(dir, 0o755); err != nil {
						t.Error(err)
					}
				})
			}
			if _, err := client.Install(ctx, release, dir); err == nil {
				t.Fatal("install succeeded with invalid input")
			}
			assertFileContents(t, target, "keep binary")
			assertNoInstallTemps(t, dir)
		})
	}
}

func TestInstallLegacyReleaseUsesCurrentNameAndPreservesLegacyFile(t *testing.T) {
	const archiveName = "claude-code-monitor_v3.0.1_linux-amd64.tar.gz"
	archive := makeArchiveEntry(t, "claude-code-monitor_v3.0.1_linux-amd64/claude-code-monitor", []byte("release executable"))
	client, _ := installationFixture(t, archiveName, archive)
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "claude-code-monitor")
	if err := os.WriteFile(legacyPath, []byte("existing legacy executable"), 0o750); err != nil {
		t.Fatal(err)
	}
	installed, err := client.Install(context.Background(), release, dir)
	if err != nil {
		t.Fatal(err)
	}
	if installed != filepath.Join(dir, "agents-otel-monitor") {
		t.Fatalf("installed path = %s, want current binary name", installed)
	}
	assertFileContents(t, installed, "release executable")
	assertFileContents(t, legacyPath, "existing legacy executable")
	assertNoInstallTemps(t, dir)
}

func installationFixture(t *testing.T, archiveName string, archive []byte) (*Updater, *Available) {
	t.Helper()
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			release := releaseResponse{TagName: "v3.0.1", Assets: []releaseAsset{
				{Name: archiveName, URL: "http://" + r.Host + "/archive"},
				{Name: "checksums.txt", URL: "http://" + r.Host + "/checksums.txt"},
			}}
			if err := json.NewEncoder(w).Encode(release); err != nil {
				t.Error(err)
			}
		case "/checksums.txt":
			writeHTTPResponse(t, w, []byte(fmt.Sprintf("%x  %s\n", digest, archiveName)))
		case "/archive":
			writeHTTPResponse(t, w, archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return testUpdater(server.Client(), server.URL+"/latest", false), &Available{
		Version: "v3.0.1", archiveName: archiveName,
		archiveURL: server.URL + "/archive", checksumURL: server.URL + "/checksums.txt",
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertNoInstallTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".agents-otel-monitor-update-") {
			t.Errorf("temporary file remains: %s", entry.Name())
		}
	}
}
