// Package openvpn owns OpenVPN-specific key and configuration generation.
package openvpn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
)

const maxCommandOutput = 64 << 10

func EnsureTLSCryptV2ServerKey(ctx context.Context, executable, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect tls-crypt-v2 server key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create tls-crypt-v2 key directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(path), ".tlscrypt-server-*")
	if err != nil {
		return fmt.Errorf("create temporary key directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmp := filepath.Join(tmpDir, "server.key")

	if err := run(ctx, executable, "--genkey", "tls-crypt-v2-server", tmp); err != nil {
		return err
	}
	data, err := readBounded(tmp, 16<<10)
	if err != nil {
		return fmt.Errorf("read generated tls-crypt-v2 server key: %w", err)
	}
	if err := atomicfile.Create(path, data, 0o600); err != nil {
		return fmt.Errorf("save tls-crypt-v2 server key: %w", err)
	}
	return nil
}

func GenerateTLSCryptV2ClientKey(ctx context.Context, executable, serverKey string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "ezgamelan-tlscrypt-client-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary key directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure temporary key directory: %w", err)
	}
	target := filepath.Join(tmpDir, "client.key")
	if err := run(
		ctx,
		executable,
		"--tls-crypt-v2", serverKey,
		"--genkey", "tls-crypt-v2-client", target,
	); err != nil {
		return nil, err
	}
	data, err := readBounded(target, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("read generated tls-crypt-v2 client key: %w", err)
	}
	return data, nil
}

func run(ctx context.Context, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run OpenVPN: %w: %s", err, output.String())
	}
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

type limitedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutput - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	if b.overflow {
		return b.buf.String() + "...(truncated)"
	}
	return b.buf.String()
}
