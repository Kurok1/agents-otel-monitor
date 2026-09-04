/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.1
 */

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/updater"
)

func TestUpdateUsesWorkingDirectoryAndExplicitInstallDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "default", want: cwd},
		{name: "absolute", args: []string{"--install-dir=" + filepath.Join(cwd, "absolute")}, want: filepath.Join(cwd, "absolute")},
		{name: "relative", args: []string{"--install-dir", "relative/nested"}, want: filepath.Join(cwd, "relative", "nested")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			installer := &recordingInstaller{}
			// Explicit updates run every time, including repeated selection of the
			// same release; the development build's version does not gate them.
			for range 2 {
				if err := runUpdate(context.Background(), tc.args, commandStreams{stdout: &stdout, stderr: &stderr}, installer); err != nil {
					t.Fatal(err)
				}
			}
			if installer.queries != 2 || installer.installs != 2 || installer.installDir != tc.want {
				t.Fatalf("calls = %+v, want two installs into %s", installer, tc.want)
			}
			want := "Installed v3.0.1 to " + filepath.Join(tc.want, "agents-otel-monitor")
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("output = %q, want %q", stdout.String(), want)
			}
			if strings.Contains(stderr.String(), "[y/N]") {
				t.Fatal("explicit update must not prompt")
			}
		})
	}
	if _, err := os.Stat("config.yaml"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("update loaded or created a config: %v", err)
	}
}

func TestUpdateRejectsInvalidArgumentsBeforeQuerying(t *testing.T) {
	for _, args := range [][]string{
		{"--install-dir="}, {"--install-dir"}, {"--unknown"}, {"some-directory"}, {"--install-dir=.", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			installer := &recordingInstaller{}
			err := runUpdate(context.Background(), args, commandStreams{stdout: io.Discard, stderr: io.Discard}, installer)
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			if installer.queries != 0 || installer.installs != 0 {
				t.Fatalf("invalid arguments invoked updater: %+v", installer)
			}
		})
	}
}

func TestUpdateHelpIsDispatchedWithoutStartingServer(t *testing.T) {
	t.Chdir(t.TempDir())
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"update", "--help"}, commandStreams{stdout: io.Discard, stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Usage of agents-otel-monitor update:") || !strings.Contains(stderr.String(), "install-dir") || !strings.Contains(stderr.String(), "working directory") {
		t.Fatalf("unexpected help: %q", stderr.String())
	}
	if err := run(context.Background(), []string{"update", "--install-dir="}, commandStreams{stdout: io.Discard, stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "install-dir") {
		t.Fatalf("update dispatch error = %v", err)
	}
}

func TestUpdatePropagatesQueryAndInstallFailures(t *testing.T) {
	failure := errors.New("fixture failure")
	for _, phase := range []string{"query", "install"} {
		t.Run(phase, func(t *testing.T) {
			var stdout bytes.Buffer
			installer := &recordingInstaller{}
			if phase == "query" {
				installer.queryErr = failure
			} else {
				installer.installErr = failure
			}
			err := runUpdate(context.Background(), nil, commandStreams{stdout: &stdout, stderr: io.Discard}, installer)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), phase) {
				t.Fatalf("error = %v, want contextual %s failure", err, phase)
			}
			if stdout.Len() != 0 {
				t.Fatalf("failure printed success: %s", stdout.String())
			}
			if phase == "query" && installer.installs != 0 {
				t.Fatal("installed after query failure")
			}
		})
	}
}

type recordingInstaller struct {
	queries    int
	installs   int
	installDir string
	queryErr   error
	installErr error
}

func (r *recordingInstaller) Latest(context.Context) (*updater.Available, error) {
	r.queries++
	if r.queryErr != nil {
		return nil, r.queryErr
	}
	return &updater.Available{Version: "v3.0.1"}, nil
}

func (r *recordingInstaller) Install(_ context.Context, _ *updater.Available, dir string) (string, error) {
	r.installs++
	r.installDir = dir
	return filepath.Join(dir, "agents-otel-monitor"), r.installErr
}
