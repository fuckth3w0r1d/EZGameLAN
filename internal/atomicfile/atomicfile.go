// Package atomicfile provides the small set of durable file operations used by
// EZGameLAN state.
package atomicfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Replace writes data to path using a temporary file, fsync and rename.
func Replace(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := writeAndSync(f, data, mode); err != nil {
		return fmt.Errorf("write temporary file for %q: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync directory %q: %w", dir, err)
	}
	return nil
}

// Create writes data atomically and reports os.ErrExist when path already
// exists. The hard-link step gives no-replace semantics on the Linux server.
func Create(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target %q is a symbolic link: %w", path, os.ErrExist)
		}
		return fmt.Errorf("target %q already exists: %w", path, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect target %q: %w", path, err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := writeAndSync(f, data, mode); err != nil {
		return fmt.Errorf("write temporary file for %q: %w", path, err)
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("target %q already exists: %w", path, os.ErrExist)
		}
		return fmt.Errorf("publish %q: %w", path, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync directory %q: %w", dir, err)
	}
	return nil
}

func writeAndSync(f *os.File, data []byte, mode os.FileMode) error {
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := f.ReadFrom(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write contents: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync contents: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	closed = true
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
