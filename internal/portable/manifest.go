// Package portable owns the on-demand elevated backend and verified VPN payload.
package portable

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func decodeManifest(encoded string) (map[string]string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var files map[string]string
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, err
	}
	if len(files) == 0 || len(files) > 32 {
		return nil, errors.New("portable payload manifest is missing or invalid")
	}
	for name, digest := range files {
		if name == "" || name == "." || strings.HasPrefix(name, "/") || filepath.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, ":") || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
			return nil, errors.New("invalid payload path")
		}
		hash, err := hex.DecodeString(digest)
		if err != nil || len(hash) != sha256.Size {
			return nil, errors.New("invalid payload digest")
		}
	}
	for _, required := range []string{"bin/openvpn.exe", "bin/tapctl.exe", "driver/OemVista.inf", "driver/tap0901.sys", "driver/tap0901.cat"} {
		if _, ok := files[required]; !ok {
			return nil, fmt.Errorf("missing payload file: %s", required)
		}
	}
	return files, nil
}

func verifiedBytes(path, digest string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("payload is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<20 {
		return nil, errors.New("payload file is too large")
	}
	hash := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), digest) {
		return nil, fmt.Errorf("payload integrity check failed: %s", filepath.Base(path))
	}
	return data, nil
}
