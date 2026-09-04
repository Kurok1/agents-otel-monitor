/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
	"github.com/kuroky/claude-code-monitor/internal/updater"
)

const (
	releaseQueryTimeout = 5 * time.Second
	installTimeout      = 2 * time.Minute
)

type releaseInstaller interface {
	Latest(context.Context) (*updater.Available, error)
	Install(context.Context, *updater.Available, string) (string, error)
}

type updateOptions struct {
	installDir string
}

func runUpdate(ctx context.Context, args []string, streams commandStreams, installer releaseInstaller) error {
	options, err := parseUpdateOptions(args, streams.stderr)
	if err != nil {
		return err
	}
	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	queryCtx, cancelQuery := context.WithTimeout(signalCtx, releaseQueryTimeout)
	available, err := installer.Latest(queryCtx)
	cancelQuery()
	if err != nil {
		return fmt.Errorf("query latest release: %w", err)
	}
	if _, err := fmt.Fprintf(streams.stderr, "Installing %s into %s...\n", available.Version, options.installDir); err != nil {
		return fmt.Errorf("write install progress: %w", err)
	}
	installCtx, cancelInstall := context.WithTimeout(signalCtx, installTimeout)
	defer cancelInstall()
	installedPath, err := installer.Install(installCtx, available, options.installDir)
	if err != nil {
		return fmt.Errorf("install release %s: %w", available.Version, err)
	}
	if _, err := fmt.Fprintf(streams.stdout, "Installed %s to %s\nRunning services continue using their current version until restarted.\n", available.Version, installedPath); err != nil {
		return fmt.Errorf("write install result: %w", err)
	}
	return nil
}

func parseUpdateOptions(args []string, stderr io.Writer) (updateOptions, error) {
	var options updateOptions
	flags := flag.NewFlagSet(buildinfo.BinaryName+" update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.installDir, "install-dir", ".", "installation directory (defaults to the current working directory)")
	if err := flags.Parse(args); err != nil {
		return updateOptions{}, fmt.Errorf("parse update flags: %w", err)
	}
	if flags.NArg() != 0 {
		return updateOptions{}, fmt.Errorf("unexpected update argument %q", flags.Arg(0))
	}
	if options.installDir == "" {
		return updateOptions{}, fmt.Errorf("install-dir must not be empty")
	}
	if !filepath.IsAbs(options.installDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return updateOptions{}, fmt.Errorf("get working directory: %w", err)
		}
		options.installDir = filepath.Join(cwd, options.installDir)
	}
	options.installDir = filepath.Clean(options.installDir)
	return options, nil
}
