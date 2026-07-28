package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/store"
)

func TestPendingClaimIsSingleUse(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	state, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	profile, client := testProfile(t, now, "alice")
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	pending := store.Pending{
		FormatVersion: 1,
		Name:          "alice",
		TokenHash:     invite.HashToken(token),
		ExpiresAt:     now.Add(30 * time.Minute),
		Profile:       profile,
	}
	if err := state.AddPending(client, pending, now); err != nil {
		t.Fatalf("AddPending: %v", err)
	}
	got, err := state.Claim(token, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.ClientName != "alice" {
		t.Fatalf("Claim client = %q", got.ClientName)
	}
	if _, err := state.Claim(token, now); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("second Claim error = %v", err)
	}
	clients, err := state.List(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].Status != store.StatusActive {
		t.Fatalf("clients = %#v", clients)
	}
}

func TestExpiredInviteReleasesName(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	state, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	profile, client := testProfile(t, now, "alice")
	token, _ := invite.NewToken()
	if err := state.AddPending(client, store.Pending{
		FormatVersion: 1,
		Name:          "alice",
		TokenHash:     invite.HashToken(token),
		ExpiresAt:     now.Add(time.Minute),
		Profile:       profile,
	}, now); err != nil {
		t.Fatal(err)
	}
	clients, err := state.List(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 0 {
		t.Fatalf("expired clients = %#v", clients)
	}
	_, replacement := testProfile(t, now.Add(2*time.Minute), "alice")
	replacement.SerialHex += "01"
	if err := state.AddActive(replacement, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("name was not released: %v", err)
	}
}

func TestClientLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	state, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxClients; i++ {
		client := store.Client{
			Name:      fmt.Sprintf("client%d", i),
			SerialHex: fmt.Sprintf("%02x", i+1),
			CreatedAt: now,
			NotAfter:  now.AddDate(1, 0, 0),
		}
		if err := state.AddActive(client, now); err != nil {
			t.Fatalf("AddActive %d: %v", i, err)
		}
	}
	err = state.AddActive(store.Client{
		Name:      "overflow",
		SerialHex: "ff",
		CreatedAt: now,
		NotAfter:  now.AddDate(1, 0, 0),
	}, now)
	if !errors.Is(err, store.ErrClientLimit) {
		t.Fatalf("overflow error = %v, want ErrClientLimit", err)
	}
}

func TestRevokeAllowsSameNameReissueAndKeepsLedger(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	state, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	_, first := testProfile(t, now, "alice")
	if err := state.AddActive(first, now); err != nil {
		t.Fatal(err)
	}
	var ledger []store.Revocation
	if err := state.Revoke("alice", now.Add(time.Minute), func(items []store.Revocation) error {
		ledger = append([]store.Revocation(nil), items...)
		return nil
	}); err != nil {
		t.Fatalf("Revoke first: %v", err)
	}
	if len(ledger) != 1 || ledger[0].SerialHex != first.SerialHex {
		t.Fatalf("first ledger = %#v", ledger)
	}

	_, second := testProfile(t, now.Add(2*time.Minute), "alice")
	if second.SerialHex == first.SerialHex {
		t.Fatal("test generated duplicate serial")
	}
	if err := state.AddActive(second, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if err := state.Revoke("alice", now.Add(3*time.Minute), func(items []store.Revocation) error {
		ledger = append([]store.Revocation(nil), items...)
		return nil
	}); err != nil {
		t.Fatalf("Revoke second: %v", err)
	}
	if len(ledger) != 2 {
		t.Fatalf("ledger = %#v, want two serials", ledger)
	}
}

func testProfile(t *testing.T, now time.Time, name string) (invite.Profile, store.Client) {
	t.Helper()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
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
	profile := invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    name,
		Endpoint:      "127.0.0.1:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\nTEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
	client := store.Client{
		Name:      name,
		SerialHex: material.SerialHex,
		CreatedAt: now,
		NotAfter:  material.NotAfter,
	}
	return profile, client
}
