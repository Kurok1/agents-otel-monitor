/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

// Package buildinfo exposes release naming and build metadata.
package buildinfo

// BinaryName is the executable name used in release archives and installations.
const BinaryName = "agents-otel-monitor"

var version = "dev" // Overridden for release builds with go build -ldflags -X.

// Version returns the version embedded at build time.
func Version() string {
	return version
}
