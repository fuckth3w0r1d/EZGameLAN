package setup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestPackAndExtract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "base.exe")
	payload := filepath.Join(root, "payload.zip")
	output := filepath.Join(root, "EZGameLAN-Setup.exe")
	if err := os.WriteFile(base, []byte("MZ-test-executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePayload(t, payload, nil)
	if err := Pack(base, payload, output); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "extracted")
	if err := Extract(output, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(
		destination,
		"payload",
		"ezgamelan-client.exe",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload/ezgamelan-client.exe" {
		t.Fatalf("client payload = %q", got)
	}
}

func TestExtractRejectsTamperedPayload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "base.exe")
	payload := filepath.Join(root, "payload.zip")
	output := filepath.Join(root, "setup.exe")
	baseData := []byte("MZ-test-executable")
	if err := os.WriteFile(base, baseData, 0o600); err != nil {
		t.Fatal(err)
	}
	writePayload(t, payload, nil)
	if err := Pack(base, payload, output); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, int64(len(baseData)+4)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Extract(output, filepath.Join(root, "out")); err == nil {
		t.Fatal("Extract accepted a tampered payload")
	}
}

func TestPackRejectsUnexpectedAndMissingFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "base.exe")
	if err := os.WriteFile(base, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]map[string]string{
		"unexpected": {"../outside": "bad"},
		"missing":    {"-third_party/openvpn.sha256": ""},
	} {
		payload := filepath.Join(root, name+".zip")
		writePayload(t, payload, extra)
		if err := Pack(base, payload, filepath.Join(root, name+".exe")); err == nil {
			t.Errorf("Pack accepted %s payload", name)
		}
	}
}

func TestExtractRejectsInvalidTrailer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "setup.exe")
	var trailer [trailerSize]byte
	copy(trailer[:8], trailerMagic[:])
	binary.LittleEndian.PutUint64(trailer[8:16], maxPayloadSize+1)
	digest := sha256.Sum256(nil)
	copy(trailer[16:], digest[:])
	if err := os.WriteFile(path, append([]byte("MZ"), trailer[:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(path, filepath.Join(root, "out")); err == nil {
		t.Fatal("Extract accepted an invalid payload size")
	}
}

func TestBuiltArtifact(t *testing.T) {
	artifact := os.Getenv("EZGAMELAN_SETUP_ARTIFACT")
	if artifact == "" {
		t.Skip("EZGAMELAN_SETUP_ARTIFACT is not set")
	}
	destination := filepath.Join(t.TempDir(), "extracted")
	if err := Extract(artifact, destination); err != nil {
		t.Fatal(err)
	}
	for name := range requiredFiles {
		extractedPath := filepath.Join(destination, filepath.FromSlash(name))
		info, err := os.Stat(extractedPath)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Errorf("%s is not a non-empty regular file", name)
		}
	}
	for _, name := range []string{"install.ps1", "uninstall.ps1"} {
		data, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
			t.Errorf("%s lacks the UTF-8 BOM required by Windows PowerShell 5.1", name)
		}
		if name == "install.ps1" {
			for _, marker := range [][]byte{
				[]byte(`$productVersion = "0.1.4"`),
				[]byte(`[string]$ProgressFile`),
				[]byte(`Set-InstallStage -Stage "install_openvpn"`),
				[]byte(`Set-InstallStage -Stage "start_service"`),
			} {
				if !bytes.Contains(data, marker) {
					t.Errorf("install.ps1 lacks progress marker %q", marker)
				}
			}
		}
	}
}

func writePayload(t *testing.T, path string, changes map[string]string) {
	t.Helper()
	files := make(map[string]string, len(requiredFiles))
	for name := range requiredFiles {
		files[name] = name
	}
	for name, data := range changes {
		if len(name) > 0 && name[0] == '-' {
			delete(files, name[1:])
			continue
		}
		files[name] = data
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, data := range files {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
