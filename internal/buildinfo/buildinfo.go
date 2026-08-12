/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

// Package buildinfo exposes metadata injected into release binaries.
package buildinfo

var version = "dev" // Overridden for release builds with go build -ldflags -X.

// Version returns the version embedded at build time.
func Version() string {
	return version
}
