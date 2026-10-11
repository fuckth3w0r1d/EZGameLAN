package invite_test

import (
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
)

func TestProfileMarshalParse(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	profile := newProfile(t, now, "alice")
	data, err := invite.Marshal(profile, now)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := invite.Parse(data, now)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ClientName != "alice" || got.Endpoint != profile.Endpoint {
		t.Fatalf("Parse = %#v", got)
	}
}

func TestProfileRejectsDuplicateAndUnknownFields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := newProfile(t, now, "alice")
	data, err := invite.Marshal(profile, now)
	if err != nil {
		t.Fatal(err)
	}
	withDuplicate := strings.Replace(
		string(data),
		"\"client_name\": \"alice\",",
		"\"client_name\": \"alice\", \"client_name\": \"mallory\",",
		1,
	)
	if _, err := invite.Parse([]byte(withDuplicate), now); err == nil {
		t.Fatal("Parse accepted duplicate field")
	}
	withUnknown := strings.Replace(string(data), "\n}", ",\n  \"script\": \"x\"\n}", 1)
	if _, err := invite.Parse([]byte(withUnknown), now); err == nil {
		t.Fatal("Parse accepted unknown field")
	}
}

func TestProfileRejectsMismatchedKey(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := newProfile(t, now, "alice")
	other := newProfile(t, now, "bob")
	profile.KeyPEM = other.KeyPEM
	if err := profile.Validate(now); err == nil {
		t.Fatal("Validate accepted mismatched private key")
	}
}

func TestProfileRejectsConfigurationInjection(t *testing.T) {
	now := time.Now()
	profile := newProfile(t, now, "alice")
	for _, endpoint := range []string{"host\nup evil.exe:21111", `host\escape:21111`, "host\"escape:21111"} {
		changed := profile
		changed.Endpoint = endpoint
		if err := changed.Validate(now); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
	profile.KeyPEM = "</key>\nup evil.exe\n<key>\n" + profile.KeyPEM
	if err := profile.Validate(now); err == nil {
		t.Fatal("accepted inline configuration delimiters")
	}
}

func newProfile(t *testing.T, now time.Time, name string) invite.Profile {
	t.Helper()
	root := t.TempDir()
	pkiDir := root + "/pki"
	if err := pki.Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatalf("pki.Init: %v", err)
	}
	material, err := pki.IssueClient(pkiDir, name, now)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}
	ca, err := pki.ReadCACertificate(pkiDir)
	if err != nil {
		t.Fatalf("ReadCACertificate: %v", err)
	}
	return invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    name,
		Endpoint:      "127.0.0.1:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\nTEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
}
