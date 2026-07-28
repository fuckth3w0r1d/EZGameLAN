// Package setup implements the bounded payload format used by the single-file
// Windows installer.
package setup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	OpenVPNPackageName = "OpenVPN-2.6.21-I001-amd64.msi"
	maxPayloadSize     = 32 << 20
	maxExtractedSize   = 64 << 20
	trailerSize        = 8 + 8 + sha256.Size
)

var (
	trailerMagic  = [8]byte{'E', 'Z', 'G', 'L', 'A', 'N', '0', '1'}
	requiredFiles = map[string]struct{}{
		"install.ps1":                       {},
		"uninstall.ps1":                     {},
		"THIRD_PARTY.txt":                   {},
		"payload/ezgamelan-client.exe":      {},
		"payload/ezgamelan-service.exe":     {},
		"third_party/openvpn.sha256":        {},
		"third_party/" + OpenVPNPackageName: {},
	}
	allowedDirectories = map[string]struct{}{
		"payload/":     {},
		"third_party/": {},
	}
)

// Pack appends a validated ZIP payload and integrity trailer to a Windows
// executable. Windows PE loaders ignore this overlay while the setup program
// reads it back through os.Executable.
func Pack(executablePath, payloadPath, outputPath string) error {
	if executablePath == "" || payloadPath == "" || outputPath == "" {
		return errors.New("setup pack paths must not be empty")
	}
	executable, err := os.Open(executablePath)
	if err != nil {
		return fmt.Errorf("open setup executable: %w", err)
	}
	defer executable.Close()
	executableInfo, err := executable.Stat()
	if err != nil || !executableInfo.Mode().IsRegular() || executableInfo.Size() <= 0 {
		return errors.New("setup executable must be a non-empty regular file")
	}

	payload, payloadSize, digest, err := openPayload(payloadPath)
	if err != nil {
		return err
	}
	defer payload.Close()

	outputDirectory := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return fmt.Errorf("create setup output directory: %w", err)
	}
	temporary, err := os.CreateTemp(outputDirectory, ".ezgamelan-setup-*.exe")
	if err != nil {
		return fmt.Errorf("create setup output: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	if _, err := io.Copy(temporary, executable); err != nil {
		return fmt.Errorf("copy setup executable: %w", err)
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind setup payload: %w", err)
	}
	if _, err := io.Copy(temporary, payload); err != nil {
		return fmt.Errorf("append setup payload: %w", err)
	}
	var trailer [trailerSize]byte
	copy(trailer[:8], trailerMagic[:])
	binary.LittleEndian.PutUint64(trailer[8:16], uint64(payloadSize))
	copy(trailer[16:], digest[:])
	if _, err := temporary.Write(trailer[:]); err != nil {
		return fmt.Errorf("append setup trailer: %w", err)
	}
	if err := temporary.Chmod(0o755); err != nil {
		return fmt.Errorf("set setup output mode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync setup output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close setup output: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("publish setup output: %w", err)
	}
	return nil
}

// Extract verifies and extracts the payload appended to executablePath.
func Extract(executablePath, destination string) error {
	executable, err := os.Open(executablePath)
	if err != nil {
		return fmt.Errorf("open setup executable: %w", err)
	}
	defer executable.Close()
	info, err := executable.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("setup executable is not a regular file")
	}
	payloadOffset, payloadSize, expectedDigest, err := readTrailer(executable, info.Size())
	if err != nil {
		return err
	}
	payload := io.NewSectionReader(executable, payloadOffset, payloadSize)
	actualDigest, err := digestReader(payload)
	if err != nil {
		return fmt.Errorf("hash setup payload: %w", err)
	}
	if !bytes.Equal(actualDigest[:], expectedDigest[:]) {
		return errors.New("setup payload integrity check failed")
	}
	archive, err := zip.NewReader(
		io.NewSectionReader(executable, payloadOffset, payloadSize),
		payloadSize,
	)
	if err != nil {
		return fmt.Errorf("open setup payload archive: %w", err)
	}
	if err := validateArchive(archive); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create setup temporary directory: %w", err)
	}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(
				filepath.Join(destination, filepath.FromSlash(file.Name)),
				0o700,
			); err != nil {
				return fmt.Errorf("create payload directory: %w", err)
			}
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create payload parent directory: %w", err)
		}
		if err := extractFile(file, target); err != nil {
			return err
		}
	}
	return nil
}

func openPayload(path string) (*os.File, int64, [sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, [sha256.Size]byte{}, fmt.Errorf("open setup payload: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		info.Size() <= 0 || info.Size() > maxPayloadSize {
		file.Close()
		return nil, 0, [sha256.Size]byte{}, errors.New(
			"setup payload must be a bounded non-empty regular file",
		)
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		file.Close()
		return nil, 0, [sha256.Size]byte{}, fmt.Errorf(
			"open setup payload archive: %w",
			err,
		)
	}
	if err := validateArchive(archive); err != nil {
		file.Close()
		return nil, 0, [sha256.Size]byte{}, err
	}
	digest, err := digestReader(io.NewSectionReader(file, 0, info.Size()))
	if err != nil {
		file.Close()
		return nil, 0, [sha256.Size]byte{}, fmt.Errorf(
			"hash setup payload: %w",
			err,
		)
	}
	return file, info.Size(), digest, nil
}

func readTrailer(
	executable *os.File,
	totalSize int64,
) (int64, int64, [sha256.Size]byte, error) {
	if totalSize < trailerSize {
		return 0, 0, [sha256.Size]byte{}, errors.New(
			"setup payload trailer is missing",
		)
	}
	var trailer [trailerSize]byte
	if _, err := executable.ReadAt(trailer[:], totalSize-trailerSize); err != nil {
		return 0, 0, [sha256.Size]byte{}, fmt.Errorf(
			"read setup payload trailer: %w",
			err,
		)
	}
	if !bytes.Equal(trailer[:8], trailerMagic[:]) {
		return 0, 0, [sha256.Size]byte{}, errors.New(
			"setup payload marker is invalid",
		)
	}
	payloadSize := int64(binary.LittleEndian.Uint64(trailer[8:16]))
	payloadOffset := totalSize - trailerSize - payloadSize
	if payloadSize <= 0 || payloadSize > maxPayloadSize || payloadOffset <= 0 {
		return 0, 0, [sha256.Size]byte{}, errors.New(
			"setup payload size is invalid",
		)
	}
	var digest [sha256.Size]byte
	copy(digest[:], trailer[16:])
	return payloadOffset, payloadSize, digest, nil
}

func validateArchive(archive *zip.Reader) error {
	if len(archive.File) == 0 || len(archive.File) > len(requiredFiles)+len(allowedDirectories) {
		return errors.New("setup payload file count is invalid")
	}
	seen := make(map[string]struct{}, len(archive.File))
	var total uint64
	for _, file := range archive.File {
		name := file.Name
		cleaned := path.Clean(name)
		expectedName := cleaned
		if file.FileInfo().IsDir() {
			expectedName += "/"
		}
		if strings.Contains(name, `\`) || path.IsAbs(name) ||
			cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
			name != expectedName {
			return fmt.Errorf("setup payload path %q is invalid", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("setup payload path %q is duplicated", name)
		}
		seen[name] = struct{}{}
		if file.FileInfo().IsDir() {
			if _, ok := allowedDirectories[name]; !ok {
				return fmt.Errorf("setup payload directory %q is not allowed", name)
			}
			continue
		}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("setup payload path %q is not a regular file", name)
		}
		if _, ok := requiredFiles[name]; !ok {
			return fmt.Errorf("setup payload file %q is not allowed", name)
		}
		total += file.UncompressedSize64
		if file.UncompressedSize64 > maxExtractedSize || total > maxExtractedSize {
			return errors.New("setup payload extracted size exceeds limit")
		}
	}
	for required := range requiredFiles {
		if _, ok := seen[required]; !ok {
			return fmt.Errorf("setup payload is missing %q", required)
		}
	}
	return nil
}

func extractFile(file *zip.File, target string) error {
	source, err := file.Open()
	if err != nil {
		return fmt.Errorf("open payload file %q: %w", file.Name, err)
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create payload file %q: %w", file.Name, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = destination.Close()
		}
	}()
	written, err := io.Copy(destination, io.LimitReader(source, maxExtractedSize+1))
	if err != nil {
		return fmt.Errorf("extract payload file %q: %w", file.Name, err)
	}
	if written != int64(file.UncompressedSize64) {
		return fmt.Errorf("payload file %q size changed during extraction", file.Name)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync payload file %q: %w", file.Name, err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close payload file %q: %w", file.Name, err)
	}
	closed = true
	return nil
}

func digestReader(reader io.Reader) ([sha256.Size]byte, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
