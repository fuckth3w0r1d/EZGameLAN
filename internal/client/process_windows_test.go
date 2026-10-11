package client

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenVPNCommandUsesSystemWindowsDirectoryAndPrivateTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SystemRoot", filepath.Join(root, "untrusted"))
	t.Setenv("TEMP", filepath.Join(root, "untrusted"))
	executable := filepath.Join(root, "bin", "openvpn.exe")
	config := filepath.Join(root, "client.ovpn")
	command, signal, err := newOpenVPNCommand(executable, config)
	if err != nil {
		t.Fatal(err)
	}
	defer signal.Close()
	windowsRoot, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if command.Dir != filepath.Dir(executable) {
		t.Fatal("unexpected DLL working directory")
	}
	environment := strings.Join(command.Env, "\n")
	if strings.Contains(environment, "untrusted") || !strings.Contains(environment, "SystemRoot="+windowsRoot) || !strings.Contains(environment, "TEMP="+root) {
		t.Fatalf("unexpected VPN environment: %s", environment)
	}
}
