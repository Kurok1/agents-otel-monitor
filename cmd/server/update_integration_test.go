/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.1
 */

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestUpdateInstallsReleaseWithoutInterruptingRunningService(t *testing.T) {
	if !(runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") || runtime.GOOS == "darwin" && runtime.GOARCH == "arm64") {
		t.Skip("platform has no official release binary")
	}
	dir := t.TempDir()
	installed := filepath.Join(dir, "agents-otel-monitor")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, current, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcListener.Close() // Also closes the reservation if setup fails early.
	statsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer statsListener.Close() // Also closes the reservation if setup fails early.
	grpcAddr, statsAddr := grpcListener.Addr().String(), statsListener.Addr().String()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	config := fmt.Sprintf("server:\n  grpc_listen: %q\nstats:\n  listen: %q\nstorage:\n  duckdb_path: %q\npricing:\n  enabled: false\n", grpcAddr, statsAddr, filepath.Join(configDir, "test.duckdb"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(configDir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close() // Fixture log is inspected only if startup fails.
	if err := grpcListener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := statsListener.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(installed, "-test.run=^TestMonitorUpdateProcessHelper$", "--", "-config", configPath)
	cmd.Env = append(os.Environ(), "MONITOR_UPDATE_PROCESS_HELPER=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // Liveness is asserted below; cleanup permits a forced exit.
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM) // The child may already have exited on a failing test.
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill() // Own test child only; guarantee the waiter exits.
			<-done
		}
	})
	var connection net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", statsAddr, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		contents, readErr := os.ReadFile(log.Name())
		t.Fatalf("server readiness: %v; log (%v): %s", err, readErr, contents)
	}
	defer connection.Close() // Persistent connection deliberately spans the update.
	reader := bufio.NewReader(connection)
	readVersion := func() string {
		t.Helper()
		if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(connection, "GET /version HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close() // Read-only fixture response.
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("version response: status=%d, err=%v", response.StatusCode, err)
		}
		return string(body)
	}
	version := readVersion()

	const payload = "new release executable fixture"
	archiveName := fmt.Sprintf("agents-otel-monitor_v3.0.1_%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	entry := strings.TrimSuffix(archiveName, ".tar.gz") + "/agents-otel-monitor"
	if err := tarWriter.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tarWriter, payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive.Bytes())
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch r.URL.Path {
		case "/repos/Kurok1/agents-otel-monitor/releases/latest":
			body = []byte(fmt.Sprintf(`{"tag_name":"v3.0.1","assets":[{"name":%q,"browser_download_url":"https://fixture.invalid/archive"},{"name":"checksums.txt","browser_download_url":"https://fixture.invalid/checksums.txt"}]}`, archiveName))
		case "/checksums.txt":
			body = []byte(fmt.Sprintf("%x  %s\n", digest, archiveName))
		case "/archive":
			body = archive.Bytes()
		default:
			t.Errorf("unexpected update request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(fixture.Close)
	fixtureURL, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real CLI dispatcher and updater, replacing only its HTTP
	// transport. This test is serial; production URLs and HTTPS guards remain.
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: fixtureReleaseTransport{destination: fixtureURL, transport: fixture.Client().Transport}}
	t.Cleanup(func() { http.DefaultClient = previousClient })
	t.Chdir(dir)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"update"}, commandStreams{stdout: &output, stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(installed)
	if err != nil || string(contents) != payload {
		t.Fatalf("installed contents = %q, err=%v", contents, err)
	}
	after, err := os.Stat(installed)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("installation did not replace the inode: %v", err)
	}
	if !strings.Contains(output.String(), "Installed v3.0.1 to "+installed) {
		t.Fatalf("unexpected update result: %s", output.String())
	}
	select {
	case <-done:
		t.Fatal("update stopped the running service")
	default:
	}
	if got := readVersion(); got != version {
		t.Fatalf("running version changed: %q -> %q", version, got)
	}
}

func TestMonitorUpdateProcessHelper(t *testing.T) {
	if os.Getenv("MONITOR_UPDATE_PROCESS_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(context.Background(), os.Args[i+1:], commandStreams{stdout: os.Stdout, stderr: os.Stderr}); err != nil {
				fmt.Fprintln(os.Stderr, err) // Child failure is captured by the parent test.
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	t.Fatal("missing helper command")
}

type fixtureReleaseTransport struct {
	destination *url.URL
	transport   http.RoundTripper
}

func (f fixtureReleaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	local := request.Clone(request.Context())
	local.URL.Scheme, local.URL.Host = f.destination.Scheme, f.destination.Host
	response, err := f.transport.RoundTrip(local)
	if response != nil {
		response.Request = request
	}
	return response, err
}
