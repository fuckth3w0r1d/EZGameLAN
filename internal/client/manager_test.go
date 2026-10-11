package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/testutil"
)

func TestNewReportsConfigurationState(t *testing.T) {
	t.Parallel()
	options := testOptions(t)
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.cancel)
	if got := manager.Status().State; got != ipc.StateNotConfigured {
		t.Fatalf("state = %q", got)
	}
	store, err := join.NewCredentialStore(options.ProfilesDir, options.OwnerSID)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(context.Background(), clientTestProfile(t, time.Now(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.cancel)
	if got := manager.Status().State; got != ipc.StateDisconnected {
		t.Fatalf("state = %q", got)
	}
	_, credentials := manager.Snapshot()
	if len(credentials) != 1 || credentials[0].ID != credential.ID || credentials[0].ClientName != "alice" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestNewRequiresPortablePathsAndOwner(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"runtime", "profiles", "owner", "openvpn", "relative runtime"} {
		t.Run(field, func(t *testing.T) {
			options := testOptions(t)
			switch field {
			case "runtime":
				options.RuntimeDir = ""
			case "profiles":
				options.ProfilesDir = ""
			case "owner":
				options.OwnerSID = ""
			case "openvpn":
				options.OpenVPNPath = ""
			case "relative runtime":
				options.RuntimeDir = "runtime"
			}
			manager, err := New(options)
			if err == nil {
				manager.cancel()
				t.Fatal("accepted incomplete portable options")
			}
		})
	}
}

func TestDeleteCredentialUpdatesListAndState(t *testing.T) {
	t.Parallel()
	options := testOptions(t)
	store, err := join.NewCredentialStore(options.ProfilesDir, options.OwnerSID)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(context.Background(), clientTestProfile(t, time.Now(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.cancel)
	response := manager.Handle(context.Background(), ipc.Request{
		Version: ipc.Version, Action: ipc.ActionDeleteCredential, CredentialID: credential.ID,
	})
	if !response.OK || response.Status.State != ipc.StateNotConfigured || len(response.Credentials) != 0 {
		t.Fatalf("delete response = %#v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
	credentials, err := store.List()
	if err != nil || len(credentials) != 0 {
		t.Fatalf("deleted credential remains: %#v, %v", credentials, err)
	}
}

func TestHandleRejectsCredentialMissingFromStartupList(t *testing.T) {
	t.Parallel()
	manager, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.cancel)
	response := manager.Handle(context.Background(), ipc.Request{
		Version: ipc.Version, Action: ipc.ActionConnect, CredentialID: "0123456789abcdef0123456789abcdef",
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
	manager, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.cancel)
	response := manager.Handle(context.Background(), ipc.Request{
		Version: ipc.Version, Action: ipc.ActionStatus,
	})
	if !response.OK || response.Status.State != ipc.StateNotConfigured {
		t.Fatalf("response = %#v", response)
	}
}

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{
		OpenVPNPath: filepath.Join(root, "openvpn"),
		ProfilesDir: filepath.Join(root, "profiles"),
		RuntimeDir:  filepath.Join(root, "runtime"),
		OwnerSID:    testutil.OwnerSID(t),
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
