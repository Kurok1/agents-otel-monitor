/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package updater

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	startupCheckTimeout = 5 * time.Second
	installTimeout      = 2 * time.Minute
)

// StartupOptions supplies the user-facing and filesystem boundaries for a
// startup update check.
type StartupOptions struct {
	CurrentVersion string
	Interactive    bool
	Input          io.Reader
	Output         io.Writer
	ExecutablePath string
}

// StartupResult reports whether startup installed a newer release.
type StartupResult struct {
	Updated        bool
	Version        string
	ExecutablePath string
}

// RunStartup checks for a newer stable release, asks an interactive user for
// confirmation, and installs the selected release. It never replaces the
// current process; the caller decides how to restart after Updated is true.
func (u *Updater) RunStartup(ctx context.Context, options StartupOptions) (StartupResult, error) {
	checkCtx, cancelCheck := context.WithTimeout(ctx, startupCheckTimeout)
	available, err := u.Check(checkCtx, options.CurrentVersion)
	cancelCheck()
	if err != nil {
		return StartupResult{}, fmt.Errorf("check latest release: %w", err)
	}
	if available == nil {
		return StartupResult{}, nil
	}

	result := StartupResult{Version: available.Version}
	if !options.Interactive {
		if _, err := fmt.Fprintf(options.Output,
			"A newer claude-code-monitor release %s is available (current %s); run it interactively to update.\n",
			available.Version, options.CurrentVersion); err != nil {
			return StartupResult{}, fmt.Errorf("write update notice: %w", err)
		}
		return result, nil
	}

	if _, err := fmt.Fprintf(options.Output,
		"A newer claude-code-monitor release %s is available (current %s). Update now? [y/N] ",
		available.Version, options.CurrentVersion); err != nil {
		return StartupResult{}, fmt.Errorf("write update prompt: %w", err)
	}
	answer, err := bufio.NewReader(options.Input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return StartupResult{}, fmt.Errorf("read update confirmation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "y") {
		return result, nil
	}

	if _, err := fmt.Fprintf(options.Output, "Downloading claude-code-monitor %s...\n", available.Version); err != nil {
		return StartupResult{}, fmt.Errorf("write download notice: %w", err)
	}
	installCtx, cancelInstall := context.WithTimeout(ctx, installTimeout)
	installedPath, err := u.Install(installCtx, available, options.ExecutablePath)
	cancelInstall()
	if err != nil {
		return StartupResult{}, fmt.Errorf("install release %s: %w", available.Version, err)
	}
	result.Updated = true
	result.ExecutablePath = installedPath
	return result, nil
}
