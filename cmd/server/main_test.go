/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package main

import (
	"bytes"
	"context"
	"testing"
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

func TestParseServerOptionsAcceptsDeprecatedUpdateFlag(t *testing.T) {
	var stderr bytes.Buffer
	plain, err := parseServerOptions(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--no-update-check", "--no-update-check=false"} {
		got, err := parseServerOptions([]string{arg}, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		if got != plain {
			t.Fatalf("deprecated flag changed options: %+v", got)
		}
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
	if !bytes.Contains(stderr.Bytes(), []byte("update [--install-dir DIR]")) {
		t.Fatalf("help = %q, want update command", stderr.String())
	}
}
