package portable

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRejectsMissingFilesAndUnsafePaths(t *testing.T) {
	hash := sha256.Sum256([]byte("payload"))
	digest := hex.EncodeToString(hash[:])
	files := map[string]string{}
	for _, path := range []string{"bin/openvpn.exe", "bin/tapctl.exe", "driver/OemVista.inf", "driver/tap0901.sys", "driver/tap0901.cat"} {
		files[path] = digest
	}
	decode := func() error {
		data, err := json.Marshal(files)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeManifest(base64.StdEncoding.EncodeToString(data))
		return err
	}
	if err := decode(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape.exe", "/escape.exe", `C:\escape.exe`, "bin/../escape.exe", "."} {
		files[path] = digest
		if err := decode(); err == nil {
			t.Errorf("accepted path %q", path)
		}
		delete(files, path)
	}
	delete(files, "bin/openvpn.exe")
	if err := decode(); err == nil {
		t.Fatal("accepted missing OpenVPN core")
	}
}

func TestPayloadRejectsModifiedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openvpn.exe")
	data := []byte("expected payload")
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedBytes(path, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedBytes(path, digest); err == nil {
		t.Fatal("accepted modified payload")
	}
}
