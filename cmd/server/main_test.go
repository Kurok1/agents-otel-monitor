/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/updater"
)

func TestVersionCommandPrintsCurrentVersionWithoutStartingServer(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"version"}, commandStreams{
		stdout: &stdout,
		stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	if got, want := stdout.String(), "dev\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionFlagPrintsCurrentVersionWithoutStartingServer(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"--version"}, commandStreams{
		stdout: &stdout,
		stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if got, want := stdout.String(), "dev\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestParseServerOptionsDisablesStartupUpdateCheck(t *testing.T) {
	var stderr bytes.Buffer

	options, err := parseServerOptions([]string{"--no-update-check"}, &stderr)
	if err != nil {
		t.Fatalf("parse --no-update-check: %v", err)
	}
	if !options.noUpdateCheck {
		t.Fatal("noUpdateCheck = false, want true")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestEnvironmentDisablesStartupUpdateCheck(t *testing.T) {
	getenv := func(key string) string {
		if key == "CLAUDE_CODE_MONITOR_NO_UPDATE_CHECK" {
			return "1"
		}
		return ""
	}

	if startupUpdateEnabled(serverOptions{}, getenv) {
		t.Fatal("startup update check enabled, want disabled by environment")
	}
}

func TestParseServerOptionsRejectsUnexpectedCommand(t *testing.T) {
	var stderr bytes.Buffer

	_, err := parseServerOptions([]string{"update"}, &stderr)
	if err == nil {
		t.Fatal("parse unexpected update command succeeded")
	}
}

func TestHelpPrintsUsageWithoutStartingServer(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"--help"}, commandStreams{
		stdout: &stdout,
		stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run --help: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("no-update-check")) {
		t.Fatalf("help = %q, want update-check flag", stderr.String())
	}
}

func TestRestartAfterUpdateExecsInstalledVersionWithOriginalProcessState(t *testing.T) {
	var stderr bytes.Buffer
	var gotPath string
	var gotArgv []string
	var gotEnv []string

	err := restartAfterUpdate(
		updater.StartupResult{
			Updated:        true,
			Version:        "v2.7.0",
			ExecutablePath: "/opt/claude-code-monitor",
		},
		restartRuntime{
			argv:        []string{"claude-code-monitor", "-config", "/etc/monitor.yaml"},
			environment: []string{"LANG=en_US.UTF-8"},
			output:      &stderr,
			exec: func(path string, argv, env []string) error {
				gotPath = path
				gotArgv = argv
				gotEnv = env
				return nil
			},
		})
	if err != nil {
		t.Fatalf("apply startup update: %v", err)
	}

	if gotPath != "/opt/claude-code-monitor" {
		t.Fatalf("exec path = %q, want /opt/claude-code-monitor", gotPath)
	}
	if got, want := len(gotArgv), 3; got != want || gotArgv[2] != "/etc/monitor.yaml" {
		t.Fatalf("exec argv = %q, want original argv", gotArgv)
	}
	if got, want := len(gotEnv), 1; got != want || gotEnv[0] != "LANG=en_US.UTF-8" {
		t.Fatalf("exec environment = %q, want original environment", gotEnv)
	}
}
