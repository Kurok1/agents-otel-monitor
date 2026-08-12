/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
	"github.com/kuroky/claude-code-monitor/internal/updater"
	"golang.org/x/term"
)

type processExec func(path string, argv, environment []string) error

type restartRuntime struct {
	argv        []string
	environment []string
	output      io.Writer
	exec        processExec
}

func maybeApplyStartupUpdate(ctx context.Context, args []string, streams commandStreams) {
	executablePath, err := os.Executable()
	if err != nil {
		writeUpdateWarning(streams.stderr, fmt.Errorf("resolve current executable: %w", err))
		return
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, os.Args[0])
	argv = append(argv, args...)
	result, err := updater.New().RunStartup(ctx, updater.StartupOptions{
		CurrentVersion: buildinfo.Version(),
		Interactive:    streamsAreInteractive(streams.stdin, streams.stderr),
		Input:          streams.stdin,
		Output:         streams.stderr,
		ExecutablePath: executablePath,
	})
	if err != nil {
		writeUpdateWarning(streams.stderr, fmt.Errorf("startup update: %w", err))
		return
	}
	if err := restartAfterUpdate(result, restartRuntime{
		argv:        argv,
		environment: os.Environ(),
		output:      streams.stderr,
		exec:        reexec,
	}); err != nil {
		writeUpdateWarning(streams.stderr, err)
	}
}

func restartAfterUpdate(result updater.StartupResult, runtime restartRuntime) error {
	if !result.Updated {
		return nil
	}
	if result.ExecutablePath == "" {
		return fmt.Errorf("restart updated executable: installed path is empty")
	}
	if _, err := fmt.Fprintf(runtime.output, "Updated to %s; restarting...\n", result.Version); err != nil {
		return fmt.Errorf("write restart notice: %w", err)
	}
	if err := runtime.exec(result.ExecutablePath, runtime.argv, runtime.environment); err != nil {
		return fmt.Errorf("run updated executable: %w", err)
	}
	return nil
}

func streamsAreInteractive(input io.Reader, output io.Writer) bool {
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	return inputOK && outputOK &&
		term.IsTerminal(int(inputFile.Fd())) && term.IsTerminal(int(outputFile.Fd()))
}

func writeUpdateWarning(output io.Writer, err error) {
	if _, writeErr := fmt.Fprintf(output, "warning: automatic update skipped: %v\n", err); writeErr != nil {
		// Startup updates are fail-open, including when their warning stream is unavailable.
		return
	}
}
