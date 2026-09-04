/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kuroky/claude-code-monitor/internal/buildinfo"
)

const (
	maxChecksumSize = 1 << 20
	maxArchiveSize  = 256 << 20
	maxBinarySize   = 512 << 20
)

// Install downloads, verifies, and atomically installs the release binary in
// installDir. It creates missing directories and returns the absolute installed
// path. An existing regular file is preserved until the final rename succeeds.
func (u *Updater) Install(ctx context.Context, available *Available, installDir string) (string, error) {
	if available == nil {
		return "", fmt.Errorf("install update: no release selected")
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("install update: %w", err)
	}
	if installDir == "" {
		return "", fmt.Errorf("install directory must not be empty")
	}
	absoluteDir, err := filepath.Abs(installDir)
	if err != nil {
		return "", fmt.Errorf("make install directory absolute: %w", err)
	}
	if err := os.MkdirAll(absoluteDir, 0o755); err != nil {
		return "", fmt.Errorf("create install directory %s: %w", absoluteDir, err)
	}
	resolvedPath := filepath.Join(absoluteDir, buildinfo.BinaryName)
	if _, err := executablePermissions(resolvedPath); err != nil {
		return "", err
	}

	checksums, err := u.download(ctx, available.checksumURL, maxChecksumSize)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	wantDigest, err := checksumFor(checksums, available.archiveName)
	if err != nil {
		return "", fmt.Errorf("select archive checksum: %w", err)
	}

	archive, err := u.download(ctx, available.archiveURL, maxArchiveSize)
	if err != nil {
		return "", fmt.Errorf("download release archive: %w", err)
	}
	gotDigest := sha256.Sum256(archive)
	if !bytes.Equal(gotDigest[:], wantDigest) {
		return "", fmt.Errorf("verify %s checksum: got %x, want %x", available.archiveName, gotDigest, wantDigest)
	}

	temp, err := os.CreateTemp(filepath.Dir(resolvedPath), "."+buildinfo.BinaryName+"-update-*")
	if err != nil {
		return "", fmt.Errorf("create replacement executable: %w", err)
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tempPath) // Best-effort cleanup; the original executable is still intact.
		}
	}()

	executableName := buildinfo.BinaryName
	if strings.HasPrefix(available.archiveName, legacyBinaryName+"_") {
		executableName = legacyBinaryName
	}
	expectedPath := strings.TrimSuffix(available.archiveName, ".tar.gz") + "/" + executableName
	if err := extractExecutable(archive, expectedPath, temp); err != nil {
		_ = temp.Close() // Preserve the extraction error; cleanup removes the temporary file.
		return "", fmt.Errorf("extract release executable: %w", err)
	}
	// Recheck the target after downloading, before committing the replacement.
	permissions, err := executablePermissions(resolvedPath)
	if err != nil {
		_ = temp.Close() // Preserve the target error; cleanup removes the temporary file.
		return "", err
	}
	if err := temp.Chmod(permissions); err != nil {
		_ = temp.Close() // Preserve the chmod error; cleanup removes the temporary file.
		return "", fmt.Errorf("preserve executable permissions: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close() // Preserve the sync error; cleanup removes the temporary file.
		return "", fmt.Errorf("sync replacement executable: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close replacement executable: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("install update: %w", err)
	}
	if err := os.Rename(tempPath, resolvedPath); err != nil {
		return "", fmt.Errorf("replace executable %s: %w", resolvedPath, err)
	}
	keepTemp = false
	return resolvedPath, nil
}

func executablePermissions(target string) (os.FileMode, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return 0o755, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect install target %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("install target %s is not a regular file (symlinks and directories are not supported)", target)
	}
	return info.Mode().Perm(), nil
}

func (u *Updater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", buildinfo.BinaryName+"-updater")

	resp, err := u.do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close() // A close error cannot affect a fully consumed read-only response.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s: unexpected HTTP status %s", url, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read %s: response exceeds %d bytes", url, limit)
	}
	return data, nil
}

func checksumFor(checksums []byte, archiveName string) ([]byte, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archiveName {
			continue
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil {
			return nil, fmt.Errorf("decode checksum for %s: %w", archiveName, err)
		}
		if len(digest) != sha256.Size {
			return nil, fmt.Errorf("checksum for %s has %d bytes, want %d", archiveName, len(digest), sha256.Size)
		}
		return digest, nil
	}
	return nil, fmt.Errorf("checksums.txt has no entry for %s", archiveName)
}

func extractExecutable(archive []byte, expectedPath string, dst io.Writer) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close() // A close error cannot change an already verified extraction.

	tarReader := tar.NewReader(gzipReader)
	found := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		cleanName := path.Clean(header.Name)
		if path.IsAbs(header.Name) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		if cleanName != expectedPath {
			continue
		}
		if found {
			return fmt.Errorf("archive contains duplicate %s entries", expectedPath)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("archive executable %s is not a regular file", expectedPath)
		}
		if header.Size <= 0 || header.Size > maxBinarySize {
			return fmt.Errorf("archive executable size %d is outside allowed range", header.Size)
		}
		written, err := io.Copy(dst, tarReader)
		if err != nil {
			return fmt.Errorf("write executable: %w", err)
		}
		if written != header.Size {
			return fmt.Errorf("write executable: wrote %d bytes, want %d", written, header.Size)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("archive has no %s entry", expectedPath)
	}
	return nil
}
