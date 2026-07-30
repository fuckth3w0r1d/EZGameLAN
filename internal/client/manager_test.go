package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/pki"
)

func TestNewReportsConfigurationState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := Options{
		OpenVPNPath:      filepath.Join(root, "openvpn"),
		LegacyConfigPath: filepath.Join(root, "ezgamelan.ovpn"),
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().State; got != ipc.StateNotConfigured {
		t.Fatalf("state = %q", got)
	}
	if err := os.WriteFile(options.LegacyConfigPath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().State; got != ipc.StateDisconnected {
		t.Fatalf("state = %q", got)
	}
	_, credentials := manager.Snapshot()
	if len(credentials) != 1 || credentials[0].ID != join.LegacyCredentialID ||
		!credentials[0].Legacy {
		t.Fatalf("legacy credentials = %#v", credentials)
	}
}

func TestDeleteCredentialUpdatesListAndState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyPath := filepath.Join(root, "ezgamelan.ovpn")
	store, err := join.NewCredentialStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(
		context.Background(),
		clientTestProfile(t, time.Now(), "alice"),
	)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{
		OpenVPNPath:      filepath.Join(root, "openvpn"),
		LegacyConfigPath: legacyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := manager.Handle(context.Background(), ipc.Request{
		Version:      ipc.Version,
		Action:       ipc.ActionDeleteCredential,
		CredentialID: credential.ID,
	})
	if !response.OK || response.Status.State != ipc.StateNotConfigured ||
		len(response.Credentials) != 0 {
		t.Fatalf("delete response = %#v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
	if _, err := store.ConfigPath(credential.ID); err == nil {
		t.Fatal("deleted configuration remains available")
	}
}

func TestHandleRejectsCredentialMissingFromStartupList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := New(Options{
		OpenVPNPath:      filepath.Join(root, "openvpn"),
		LegacyConfigPath: filepath.Join(root, "ezgamelan.ovpn"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := manager.Handle(context.Background(), ipc.Request{
		Version:      ipc.Version,
		Action:       ipc.ActionConnect,
		CredentialID: "0123456789abcdef0123456789abcdef",
	})
	if response.OK || response.Code != "E_CREDENTIAL_NOT_FOUND" {
		t.Fatalf("response = %#v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
}

func TestHandleStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := New(Options{
		OpenVPNPath:      filepath.Join(root, "openvpn"),
		LegacyConfigPath: filepath.Join(root, "ezgamelan.ovpn"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := manager.Handle(context.Background(), ipc.Request{
		Version: ipc.Version,
		Action:  ipc.ActionStatus,
	})
	if !response.OK || response.Status.State != ipc.StateNotConfigured {
		t.Fatalf("response = %#v", response)
	}
}

func TestRestartDelayIsBounded(t *testing.T) {
	t.Parallel()
	if got := restartDelay(0); got != time.Second {
		t.Fatalf("first delay = %s", got)
	}
	if got := restartDelay(10); got != 15*time.Second {
		t.Fatalf("bounded delay = %s", got)
	}
}

func TestRenameVirtualAdapterRecordsOriginalName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	marker := filepath.Join(root, "adapter-original-name.txt")
	manager, err := New(Options{
		OpenVPNPath:       filepath.Join(root, "openvpn"),
		LegacyConfigPath:  filepath.Join(root, "ezgamelan.ovpn"),
		AdapterMarkerPath: marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	const original = "OpenVPN TAP-Windows6"
	if got := manager.renameVirtualAdapter(original, true); got != "EZGameLAN" {
		t.Fatalf("adapter name = %q", got)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original+"\n" {
		t.Fatalf("marker = %q", data)
	}
}

func TestRenameVirtualAdapterSkipsUnconfirmedInterface(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	marker := filepath.Join(root, "adapter-original-name.txt")
	manager, err := New(Options{
		OpenVPNPath:       filepath.Join(root, "openvpn"),
		LegacyConfigPath:  filepath.Join(root, "ezgamelan.ovpn"),
		AdapterMarkerPath: marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.renameVirtualAdapter("Ethernet", false); got != "Ethernet" {
		t.Fatalf("adapter name = %q", got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected marker: %v", err)
	}
}

func TestBestEffortWriterDoesNotPropagateLogFailure(t *testing.T) {
	t.Parallel()
	writer := bestEffortWriter{writer: errorWriter{}}
	data := []byte("OpenVPN output")
	written, err := writer.Write(data)
	if err != nil || written != len(data) {
		t.Fatalf("Write = %d, %v", written, err)
	}
}

func TestErrorCodeDoesNotIncludeDetails(t *testing.T) {
	t.Parallel()
	err := errors.New("E_JOIN_TOKEN: secret detail")
	if got := errorCode(err); got != "E_JOIN_TOKEN" {
		t.Fatalf("code = %q", got)
	}
	if strings.Contains(userMessage(errorCode(err)), "secret") {
		t.Fatal("user message included internal details")
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("test log failure")
}

func clientTestProfile(t *testing.T, now time.Time, name string) invite.Profile {
	t.Helper()
	pkiDir := filepath.Join(t.TempDir(), "pki")
	if err := pki.Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatal(err)
	}
	material, err := pki.IssueClient(pkiDir, name, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.ReadCACertificate(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	return invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    name,
		Endpoint:      "vpn.example.test:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\n" +
			"TEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
}
