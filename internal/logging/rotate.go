// Package logging provides the small bounded logs used by the Windows client.
package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	DefaultMaxFileSize = int64(256 << 10)
	DefaultBackups     = 2
	UserMaxFileSize    = int64(128 << 10)
	UserBackups        = 1
)

var (
	tokenValuePattern = regexp.MustCompile(
		`(?i)(token[[:space:]"']*[:=][[:space:]"']*)([A-Za-z0-9_-]{20,})`,
	)
	linkTokenPattern = regexp.MustCompile(`#[A-Za-z0-9_-]{20,}`)
)

// RotatingFile retains the current log and a small fixed number of backups.
type RotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
	closed  bool
	protect bool
}

func Open(path string) (*RotatingFile, error) {
	return open(path, DefaultMaxFileSize, DefaultBackups)
}

// OpenUser creates a smaller log under the current user's profile. It relies on
// that profile's inherited ACL instead of replacing it with a service-only ACL.
func OpenUser(path string) (*RotatingFile, error) {
	return openWithSecurity(path, UserMaxFileSize, UserBackups, false)
}

func open(path string, maxSize int64, backups int) (*RotatingFile, error) {
	return openWithSecurity(path, maxSize, backups, true)
}

func openWithSecurity(
	path string,
	maxSize int64,
	backups int,
	protect bool,
) (*RotatingFile, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("log path must be absolute")
	}
	if maxSize < 1024 || backups < 0 || backups > 10 {
		return nil, errors.New("invalid log rotation limits")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	if protect {
		if err := securePath(directory, true); err != nil {
			return nil, fmt.Errorf("protect log directory: %w", err)
		}
	}
	file, size, err := openFile(path)
	if err != nil {
		return nil, err
	}
	if protect {
		if err := securePath(path, false); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("protect log file: %w", err)
		}
	}
	return &RotatingFile{
		path:    path,
		maxSize: maxSize,
		backups: backups,
		file:    file,
		size:    size,
		protect: protect,
	}, nil
}

func openFile(path string) (*os.File, int64, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, 0, errors.New("log path is a symbolic link")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, fmt.Errorf("inspect log file: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("stat log file: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, errors.New("log path is not a regular file")
	}
	return file, info.Size(), nil
}

func (r *RotatingFile) Write(data []byte) (int, error) {
	originalSize := len(data)
	if originalSize == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if int64(len(data)) > r.maxSize {
		data = data[len(data)-int(r.maxSize):]
	}
	if r.size > 0 && r.size+int64(len(data)) > r.maxSize {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	if err := writeAll(r.file, data); err != nil {
		return 0, fmt.Errorf("write log: %w", err)
	}
	r.size += int64(len(data))
	return originalSize, nil
}

func (r *RotatingFile) rotateLocked() (resultErr error) {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("close log before rotation: %w", err)
	}
	r.file = nil
	defer func() {
		if r.file != nil {
			return
		}
		file, size, err := openFile(r.path)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			return
		}
		r.file = file
		r.size = size
	}()
	if r.backups == 0 {
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove rotated log: %w", err)
		}
	} else {
		oldest := backupPath(r.path, r.backups)
		if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove oldest log: %w", err)
		}
		for index := r.backups - 1; index >= 1; index-- {
			source := backupPath(r.path, index)
			target := backupPath(r.path, index+1)
			if err := os.Rename(source, target); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("rotate log backup: %w", err)
			}
		}
		if err := os.Rename(r.path, backupPath(r.path, 1)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate current log: %w", err)
		}
	}
	file, size, err := openFile(r.path)
	if err != nil {
		return err
	}
	r.file = file
	r.size = size
	if r.protect {
		if err := securePath(r.path, false); err != nil {
			return fmt.Errorf("protect rotated log: %w", err)
		}
	}
	return nil
}

// Snapshot returns the newest log bytes up to limit in chronological order.
func (r *RotatingFile) Snapshot(limit int) (string, error) {
	if limit <= 0 {
		return "", errors.New("snapshot limit must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", os.ErrClosed
	}
	if err := r.file.Sync(); err != nil {
		return "", fmt.Errorf("sync log: %w", err)
	}
	result := make([]byte, 0, limit)
	for index := r.backups; index >= 1; index-- {
		data, err := readBounded(backupPath(r.path, index), r.maxSize)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("read log backup: %w", err)
		}
		result = appendNewest(result, data, limit)
	}
	data, err := readBounded(r.path, r.maxSize)
	if err != nil {
		return "", fmt.Errorf("read current log: %w", err)
	}
	result = appendNewest(result, data, limit)
	return truncateUTF8(Sanitize(result), limit), nil
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.file.Close()
}

func backupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

func appendNewest(current, data []byte, limit int) []byte {
	if len(data) >= limit {
		return append(current[:0], data[len(data)-limit:]...)
	}
	if overflow := len(current) + len(data) - limit; overflow > 0 {
		copy(current, current[overflow:])
		current = current[:len(current)-overflow]
	}
	return append(current, data...)
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		if _, err := file.Seek(-limit, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

// Sanitize removes invitation credentials and private key blocks before export.
func Sanitize(data []byte) string {
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	text = tokenValuePattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = linkTokenPattern.ReplaceAllString(text, "#[REDACTED]")

	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	inSecretBlock := false
	for _, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !inSecretBlock && ((strings.Contains(upper, "BEGIN") &&
			(strings.Contains(upper, "PRIVATE KEY") ||
				strings.Contains(upper, "TLS-CRYPT"))) ||
			upper == "<KEY>" || upper == "<TLS-CRYPT-V2>") {
			filtered = append(filtered, "[REDACTED KEY MATERIAL]")
			inSecretBlock = true
			continue
		}
		if inSecretBlock {
			if (strings.Contains(upper, "END") &&
				(strings.Contains(upper, "PRIVATE KEY") ||
					strings.Contains(upper, "TLS-CRYPT"))) ||
				upper == "</KEY>" || upper == "</TLS-CRYPT-V2>" {
				inSecretBlock = false
			}
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[len(value)-limit:]
	for len(value) > 0 && value[0]&0xC0 == 0x80 {
		value = value[1:]
	}
	return value
}
