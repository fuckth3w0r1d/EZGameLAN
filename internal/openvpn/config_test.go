package openvpn_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/openvpn"
	"github.com/r3t2/ezgamelan/internal/pki"
)

func TestServerConfigUsesPureTAP(t *testing.T) {
	t.Parallel()
	cfg := config.Default("vpn.example.test")
	cfg.StateDir = "/var/lib/ezgamelan"
	cfg.PKIDir = "/etc/ezgamelan/pki"
	got, err := openvpn.ServerConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dev tap\n",
		"client-to-client\n",
		"max-clients 10\n",
		"ifconfig-pool 10.86.0.10 10.86.0.19 255.255.255.0\n",
		"port 21111\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"redirect-gateway", "dhcp-option", "dev tun", "server-bridge"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("config contains %q:\n%s", unwanted, got)
		}
	}
}

func TestClientConfigIsFixed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := configProfile(t, now)
	got, err := openvpn.ClientConfig(profile, 1400)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dev tap\n",
		"windows-driver tap-windows6\n",
		"remote \"127.0.0.1\" 21111\n",
		"verify-x509-name \"ezgamelan-server\" name\n",
		"pull-filter ignore redirect-gateway\n",
		"<key>\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q", want)
		}
	}
}

func configProfile(t *testing.T, now time.Time) invite.Profile {
	t.Helper()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	if err := pki.Init(pkiDir, filepath.Join(root, "enroll"), "127.0.0.1", "ezgamelan-server", now); err != nil {
		t.Fatal(err)
	}
	material, err := pki.IssueClient(pkiDir, "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.ReadCACertificate(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	return invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    "alice",
		Endpoint:      "127.0.0.1:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\nTEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
}
